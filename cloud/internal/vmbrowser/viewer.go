package vmbrowser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/coder/websocket"
)

const (
	defaultViewerWidth  = 1280
	defaultViewerHeight = 720
	minViewerWidth      = 320
	minViewerHeight     = 240
	maxViewerWidth      = 1440
	maxViewerHeight     = 900
	viewerControlBuffer = 64
)

type chromiumStarter interface {
	EnsureRunning(context.Context) (Endpoint, error)
}

type ViewerControllerOptions struct {
	Chromium chromiumStarter
	Engine   EngineLike
	Arbiter  *ControlArbiter
	DialCDP  cdpDialer
	Logger   *slog.Logger
}

// ViewerController owns the second, restricted CDP session used for the Cloud
// viewer. It permits one loopback viewer connection and never exposes raw CDP.
type ViewerController struct {
	opts    ViewerControllerOptions
	mu      sync.Mutex
	active  bool
	session *viewerSession
	epoch   atomic.Uint64
}

type viewerSession struct {
	ctx            context.Context
	cancel         context.CancelFunc
	cdp            cdpConnection
	control        chan browserstream.Control
	frames         *browserstream.Latest
	writeMu        sync.Mutex
	opMu           sync.Mutex
	sessionID      string
	targetID       string
	width          int
	height         int
	sequence       uint64
	epoch          uint64
	started        time.Time
	rateMu         sync.Mutex
	rateStart      time.Time
	rateCount      int
	loading        bool
	dialogOpen     bool
	dialogType     string
	dialogText     string
	dialogPrompt   string
	quality        int
	fps            int
	captureWidth   int
	captureHeight  int
	lastFrame      time.Time
	deferredFrame  *deferredViewerFrame
	deferredFlush  bool
	saturatedSince time.Time
	stableSince    time.Time
	attachStarted  time.Time
	chromiumReady  time.Duration
	framesCaptured atomic.Uint64
	framesReplaced atomic.Uint64
	framesSent     atomic.Uint64
	bytesSent      atomic.Uint64
	inputRejected  atomic.Uint64
}

type deferredViewerFrame struct {
	jpeg       []byte
	capturedAt time.Time
	targetID   string
	width      int
	height     int
}

func NewViewerController(opts ViewerControllerOptions) *ViewerController {
	if opts.Arbiter == nil {
		opts.Arbiter = NewControlArbiter(nil)
	}
	if opts.DialCDP == nil {
		opts.DialCDP = dialWebsocketCDP
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	controller := &ViewerController{opts: opts}
	controller.epoch.Store(uint64(time.Now().UnixNano()))
	return controller
}

func (v *ViewerController) Attached() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active
}

func (v *ViewerController) Serve(ctx context.Context, conn *websocket.Conn) error {
	attachStarted := time.Now()
	v.mu.Lock()
	if v.active {
		v.mu.Unlock()
		return errors.New("a browser viewer is already attached")
	}
	v.active = true
	v.mu.Unlock()
	defer func() {
		v.opts.Arbiter.ReleaseUser()
		v.mu.Lock()
		v.active = false
		v.session = nil
		v.mu.Unlock()
	}()

	endpoint, err := v.opts.Chromium.EnsureRunning(ctx)
	if err != nil {
		return fmt.Errorf("start Chromium for viewer: %w", err)
	}
	cdp, err := v.opts.DialCDP(ctx, endpoint.WebSocketURL)
	if err != nil {
		return err
	}
	defer cdp.Close()

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	epoch := v.epoch.Add(1)
	state := &viewerSession{
		ctx: sessionCtx, cancel: cancel, cdp: cdp,
		control: make(chan browserstream.Control, viewerControlBuffer),
		frames:  browserstream.NewLatest(), width: defaultViewerWidth,
		height: defaultViewerHeight, epoch: epoch, started: time.Now(),
		quality: 70, fps: 15, captureWidth: maxViewerWidth, captureHeight: maxViewerHeight,
		attachStarted: attachStarted, chromiumReady: time.Since(attachStarted),
	}
	defer state.frames.Close()
	defer func() {
		v.opts.Logger.Info("browser viewer session closed",
			"duration_ms", time.Since(state.attachStarted).Milliseconds(),
			"chromium_ready_ms", state.chromiumReady.Milliseconds(),
			"frames_captured", state.framesCaptured.Load(),
			"frames_replaced", state.framesReplaced.Load(),
			"frames_sent", state.framesSent.Load(),
			"bytes_sent", state.bytesSent.Load(),
			"input_rejected", state.inputRejected.Load(),
		)
	}()
	v.mu.Lock()
	v.session = state
	v.mu.Unlock()

	if err := v.attachInitialTarget(state); err != nil {
		return err
	}
	conn.SetReadLimit(browserstream.MaxControlBytes)
	writeErr := make(chan error, 2)
	go func() { writeErr <- v.writeControls(conn, state) }()
	go func() { writeErr <- v.writeFrames(conn, state) }()
	go v.readCDPEvents(state)

	v.enqueue(state, browserstream.Control{
		Type: "hello", Version: browserstream.Version, StreamEpoch: epoch,
	})
	v.enqueue(state, browserstream.Control{
		Type: "attached", Version: browserstream.Version, StreamEpoch: epoch,
		TargetID: state.targetID, Width: state.width, Height: state.height, Running: true,
	})
	v.publishState(state)

	readErr := make(chan error, 1)
	go func() { readErr <- v.readViewerControls(conn, state) }()
	select {
	case err := <-readErr:
		cancel()
		return err
	case err := <-writeErr:
		cancel()
		return err
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

func (v *ViewerController) attachInitialTarget(state *viewerSession) error {
	if err := state.cdp.Call(state.ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return err
	}
	tabs, err := v.listTargets(state)
	if err != nil {
		return err
	}
	targetID := ""
	for _, tab := range tabs {
		if tab.Active {
			targetID = tab.ID
			break
		}
	}
	if targetID == "" && len(tabs) > 0 {
		targetID = tabs[0].ID
	}
	if targetID == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := state.cdp.Call(state.ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return err
		}
		targetID = created.TargetID
	}
	return v.switchTarget(state, targetID)
}

func (v *ViewerController) switchTarget(state *viewerSession, targetID string) error {
	state.opMu.Lock()
	if targetID == "" {
		state.opMu.Unlock()
		return errors.New("browser viewer target is required")
	}
	if state.sessionID != "" {
		_ = state.cdp.Call(state.ctx, state.sessionID, "Page.stopScreencast", nil, nil)
		_ = state.cdp.Call(state.ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": state.sessionID}, nil)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := state.cdp.Call(state.ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID, "flatten": true,
	}, &attached); err != nil {
		state.opMu.Unlock()
		return err
	}
	if attached.SessionID == "" {
		state.opMu.Unlock()
		return errors.New("Chromium returned no target session")
	}
	state.targetID = targetID
	state.sessionID = attached.SessionID
	state.lastFrame = time.Time{}
	state.saturatedSince = time.Time{}
	state.stableSince = time.Time{}
	state.dialogOpen = false
	state.dialogType = ""
	state.dialogText = ""
	state.dialogPrompt = ""
	if err := state.cdp.Call(state.ctx, "", "Target.activateTarget", map[string]any{"targetId": targetID}, nil); err != nil {
		state.opMu.Unlock()
		return err
	}
	if err := state.cdp.Call(state.ctx, state.sessionID, "Page.enable", nil, nil); err != nil {
		state.opMu.Unlock()
		return err
	}
	if err := v.applyViewportLocked(state); err != nil {
		state.opMu.Unlock()
		return err
	}
	newSessionID := state.sessionID
	state.opMu.Unlock()

	// State is queued before screencast starts so a new-target frame is never
	// presented under the previous tab metadata.
	v.publishState(state)
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if state.sessionID != newSessionID || state.targetID != targetID {
		return nil
	}
	return v.startScreencastLocked(state)
}

func (v *ViewerController) startScreencastLocked(state *viewerSession) error {
	return state.cdp.Call(state.ctx, state.sessionID, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": state.quality,
		"maxWidth":  min(state.width, state.captureWidth),
		"maxHeight": min(state.height, state.captureHeight), "everyNthFrame": 1,
	}, nil)
}

func (v *ViewerController) applyViewportLocked(state *viewerSession) error {
	return state.cdp.Call(state.ctx, state.sessionID, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": state.width, "height": state.height, "deviceScaleFactor": 1,
		"mobile": false,
	}, nil)
}

func (v *ViewerController) readCDPEvents(state *viewerSession) {
	defer state.cancel()
	for event := range state.cdp.Events() {
		if state.ctx.Err() != nil {
			return
		}
		switch event.Method {
		case "Page.screencastFrame":
			v.acceptScreencastFrame(state, event)
		case "Target.targetCreated":
			v.handleTargetCreated(state, event)
		case "Target.targetDestroyed":
			v.handleTargetDestroyed(state, event)
		case "Target.targetInfoChanged":
			v.publishState(state)
		case "Page.frameStartedLoading":
			state.opMu.Lock()
			state.loading = true
			state.opMu.Unlock()
			v.publishState(state)
		case "Page.loadEventFired", "Page.frameStoppedLoading":
			state.opMu.Lock()
			state.loading = false
			state.opMu.Unlock()
			v.publishState(state)
		case "Page.javascriptDialogOpening":
			var dialog struct {
				Type          string `json:"type"`
				Message       string `json:"message"`
				DefaultPrompt string `json:"defaultPrompt"`
			}
			if json.Unmarshal(event.Params, &dialog) == nil {
				state.opMu.Lock()
				state.dialogOpen = true
				state.dialogType = dialog.Type
				state.dialogText = dialog.Message
				state.dialogPrompt = dialog.DefaultPrompt
				state.opMu.Unlock()
				v.publishState(state)
			}
		case "Page.javascriptDialogClosed":
			state.opMu.Lock()
			state.dialogOpen = false
			state.dialogType = ""
			state.dialogText = ""
			state.dialogPrompt = ""
			state.opMu.Unlock()
			v.publishState(state)
		case "Inspector.targetCrashed":
			v.enqueue(state, browserstream.Control{
				Type: "error", Version: browserstream.Version, StreamEpoch: state.epoch,
				Code: "BROWSER_RESTARTING", Message: "The browser is restarting.",
			})
			v.opts.Arbiter.Reset()
			state.cancel()
		}
	}
}

func (v *ViewerController) handleTargetCreated(state *viewerSession, event cdpEvent) {
	var created struct {
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			OpenerID string `json:"openerId"`
		} `json:"targetInfo"`
	}
	if json.Unmarshal(event.Params, &created) != nil {
		return
	}
	state.opMu.Lock()
	activeTarget := state.targetID
	state.opMu.Unlock()
	if created.TargetInfo.Type == "page" && created.TargetInfo.TargetID != "" && created.TargetInfo.OpenerID == activeTarget {
		if v.opts.Engine != nil && v.opts.Arbiter.Owner() == ControlUser {
			if _, err := v.opts.Engine.Execute(state.ctx, "tab-select", map[string]any{"tabId": created.TargetInfo.TargetID}); err != nil {
				v.opts.Logger.Debug("sync browser engine to user popup", "error", err)
			}
		}
		if err := v.switchTarget(state, created.TargetInfo.TargetID); err != nil {
			v.opts.Logger.Debug("switch browser viewer to popup", "error", err)
		}
		return
	}
	v.publishState(state)
}

func (v *ViewerController) handleTargetDestroyed(state *viewerSession, event cdpEvent) {
	var destroyed struct {
		TargetID string `json:"targetId"`
	}
	if json.Unmarshal(event.Params, &destroyed) != nil {
		return
	}
	state.opMu.Lock()
	wasActive := destroyed.TargetID != "" && destroyed.TargetID == state.targetID
	state.opMu.Unlock()
	if !wasActive {
		v.publishState(state)
		return
	}
	tabs, err := v.listTargets(state)
	if err != nil {
		return
	}
	if len(tabs) == 0 {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := state.cdp.Call(state.ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return
		}
		tabs = append(tabs, browserstream.Tab{ID: created.TargetID})
	}
	if err := v.switchTarget(state, tabs[0].ID); err != nil {
		v.opts.Logger.Debug("recover browser viewer after tab close", "error", err)
	}
}

func (v *ViewerController) acceptScreencastFrame(state *viewerSession, event cdpEvent) {
	state.opMu.Lock()
	activeSession := state.sessionID
	state.opMu.Unlock()
	if event.SessionID != "" && event.SessionID != activeSession {
		return
	}
	var params struct {
		Data      string `json:"data"`
		SessionID int    `json:"sessionId"`
	}
	if json.Unmarshal(event.Params, &params) != nil || params.Data == "" || params.SessionID <= 0 {
		return
	}
	// Ack before any relay work. The latest-frame slot bounds memory if every
	// downstream viewer is slower than Chromium.
	if err := state.cdp.Call(state.ctx, state.sessionID, "Page.screencastFrameAck", map[string]any{
		"sessionId": params.SessionID,
	}, nil); err != nil {
		return
	}
	jpeg, err := base64.StdEncoding.DecodeString(params.Data)
	if err != nil || len(jpeg) == 0 {
		return
	}
	if len(jpeg) > browserstream.MaxFrameBytes {
		v.adaptViewer(state, true, time.Now())
		return
	}
	now := time.Now()
	state.opMu.Lock()
	pending := &deferredViewerFrame{
		jpeg: append([]byte(nil), jpeg...), capturedAt: now,
		targetID: state.targetID, width: state.width, height: state.height,
	}
	if state.deferredFlush {
		state.deferredFrame = pending
		state.opMu.Unlock()
		return
	}
	if !state.lastFrame.IsZero() {
		remaining := time.Second/time.Duration(state.fps) - now.Sub(state.lastFrame)
		if remaining > 0 {
			state.deferredFrame = pending
			state.deferredFlush = true
			state.opMu.Unlock()
			time.AfterFunc(remaining, func() { v.flushDeferredFrame(state) })
			return
		}
	}
	state.lastFrame = now
	state.sequence++
	frame := browserstream.Frame{
		StreamEpoch: state.epoch, Sequence: state.sequence,
		Width: uint16(state.width), Height: uint16(state.height),
		CapturedMS: uint64(time.Since(state.started).Milliseconds()), TargetID: state.targetID, JPEG: jpeg,
	}
	quality, fps := state.quality, state.fps
	state.opMu.Unlock()
	v.publishViewerFrame(state, frame, quality, fps, now)
}

func (v *ViewerController) flushDeferredFrame(state *viewerSession) {
	now := time.Now()
	state.opMu.Lock()
	pending := state.deferredFrame
	state.deferredFrame = nil
	state.deferredFlush = false
	if pending == nil || state.ctx.Err() != nil || pending.targetID != state.targetID {
		state.opMu.Unlock()
		return
	}
	state.lastFrame = now
	state.sequence++
	frame := browserstream.Frame{
		StreamEpoch: state.epoch, Sequence: state.sequence,
		Width: uint16(pending.width), Height: uint16(pending.height),
		CapturedMS: uint64(pending.capturedAt.Sub(state.started).Milliseconds()),
		TargetID:   pending.targetID, JPEG: pending.jpeg,
	}
	quality, fps := state.quality, state.fps
	state.opMu.Unlock()
	v.publishViewerFrame(state, frame, quality, fps, now)
}

func (v *ViewerController) publishViewerFrame(
	state *viewerSession,
	frame browserstream.Frame,
	quality int,
	fps int,
	now time.Time,
) {
	encoded, err := browserstream.EncodeFrame(frame)
	if err == nil {
		replaced := state.frames.Put(encoded)
		captured := state.framesCaptured.Add(1)
		if replaced {
			state.framesReplaced.Add(1)
		}
		if captured == 1 {
			v.opts.Logger.Info("browser viewer first frame",
				"attach_to_frame_ms", time.Since(state.attachStarted).Milliseconds(),
				"chromium_ready_ms", state.chromiumReady.Milliseconds(),
				"frame_bytes", len(frame.JPEG), "width", frame.Width, "height", frame.Height,
				"quality", quality, "fps", fps,
			)
		}
		v.adaptViewer(state, replaced, now)
	}
}

func (v *ViewerController) adaptViewer(state *viewerSession, saturated bool, now time.Time) {
	state.opMu.Lock()
	changed := false
	if saturated {
		state.stableSince = time.Time{}
		if state.saturatedSince.IsZero() {
			state.saturatedSince = now
		}
		if now.Sub(state.saturatedSince) >= 3*time.Second {
			switch {
			case state.fps > 10:
				state.fps = 10
				changed = true
			case state.fps > 6:
				state.fps = 6
				changed = true
			case state.quality > 55:
				state.quality = 55
				changed = true
			case state.captureWidth > 1280 || state.captureHeight > 720:
				state.captureWidth, state.captureHeight = 1280, 720
				changed = true
			}
			state.saturatedSince = now
		}
	} else {
		state.saturatedSince = time.Time{}
		if state.stableSince.IsZero() {
			state.stableSince = now
		}
		if now.Sub(state.stableSince) >= 10*time.Second {
			switch {
			case state.captureWidth < maxViewerWidth || state.captureHeight < maxViewerHeight:
				state.captureWidth, state.captureHeight = maxViewerWidth, maxViewerHeight
				changed = true
			case state.quality < 70:
				state.quality = 70
				changed = true
			case state.fps < 10:
				state.fps = 10
				changed = true
			case state.fps < 15:
				state.fps = 15
				changed = true
			}
			state.stableSince = now
		}
	}
	if !changed || state.sessionID == "" {
		state.opMu.Unlock()
		return
	}
	_ = state.cdp.Call(state.ctx, state.sessionID, "Page.stopScreencast", nil, nil)
	_ = v.startScreencastLocked(state)
	v.opts.Logger.Info("browser viewer adapted",
		"quality", state.quality, "fps", state.fps,
		"capture_width", state.captureWidth, "capture_height", state.captureHeight,
	)
	state.opMu.Unlock()
	v.publishState(state)
}

func (v *ViewerController) readViewerControls(conn *websocket.Conn, state *viewerSession) error {
	for {
		kind, payload, err := conn.Read(state.ctx)
		if err != nil {
			return err
		}
		if kind != websocket.MessageText || len(payload) == 0 || len(payload) > browserstream.MaxControlBytes {
			return errors.New("invalid browser viewer control frame")
		}
		var control browserstream.Control
		if json.Unmarshal(payload, &control) != nil || control.Version != browserstream.Version {
			return errors.New("invalid browser viewer control message")
		}
		v.handleControl(state, control)
	}
}

func (v *ViewerController) handleControl(state *viewerSession, control browserstream.Control) {
	if control.Type != "ping" && control.Type != "detach" && !allowViewerInput(state, time.Now()) {
		v.enqueue(state, browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: "BROWSER_INPUT_RATE_EXCEEDED",
		})
		return
	}
	active := control.Type != "input" || control.Kind != "pointerMove" || control.Buttons != 0
	if control.Type != "ping" && !v.opts.Arbiter.TryUser(active) {
		v.enqueue(state, browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: "BROWSER_AGENT_CONTROL_ACTIVE", Owner: string(ControlAgent),
		})
		return
	}
	minFrame := uint64(0)
	if control.InputSeq > 0 {
		state.opMu.Lock()
		minFrame = state.sequence + 1
		state.opMu.Unlock()
	}
	var err error
	switch control.Type {
	case "ping":
		v.enqueue(state, browserstream.Control{Type: "pong", Version: browserstream.Version, StreamEpoch: state.epoch})
		return
	case "viewport":
		err = v.resize(state, control.Width, control.Height)
		if err == nil {
			v.enqueue(state, browserstream.Control{
				Type: "viewport_ack", Version: browserstream.Version, StreamEpoch: state.epoch,
				Width: state.width, Height: state.height,
			})
		}
	case "input":
		err = v.dispatchInput(state, control)
	case "navigate":
		err = v.navigate(state, control)
	case "tab":
		err = v.tabOperation(state, control)
	case "dialog":
		err = v.dialogOperation(state, control)
	case "detach":
		state.cancel()
		return
	default:
		err = errors.New("unsupported browser viewer control type")
	}
	if err != nil {
		state.inputRejected.Add(1)
		v.enqueue(state, browserstream.Control{
			Type: "input_rejected", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, Code: "BROWSER_INPUT_REJECTED", Message: "Browser input could not be applied.",
		})
		v.opts.Logger.Debug("browser viewer input rejected", "error", err, "type", control.Type, "kind", control.Kind)
		return
	}
	if control.InputSeq > 0 {
		v.enqueue(state, browserstream.Control{
			Type: "input_ack", Version: browserstream.Version, StreamEpoch: state.epoch,
			InputSeq: control.InputSeq, MinFrameSeq: minFrame, Accepted: true,
		})
	}
	v.enqueue(state, browserstream.Control{
		Type: "control_owner", Version: browserstream.Version,
		StreamEpoch: state.epoch, Owner: string(v.opts.Arbiter.Owner()),
	})
	go func(epoch uint64) {
		timer := time.NewTimer(defaultUserControlLease + 25*time.Millisecond)
		defer timer.Stop()
		select {
		case <-state.ctx.Done():
		case <-timer.C:
			v.enqueue(state, browserstream.Control{
				Type: "control_owner", Version: browserstream.Version,
				StreamEpoch: epoch, Owner: string(v.opts.Arbiter.Owner()),
			})
		}
	}(state.epoch)
}

func (v *ViewerController) resize(state *viewerSession, width, height int) error {
	if width < minViewerWidth || width > maxViewerWidth || height < minViewerHeight || height > maxViewerHeight {
		return errors.New("browser viewport is outside the allowed range")
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	state.width, state.height = width, height
	if err := v.applyViewportLocked(state); err != nil {
		return err
	}
	if err := state.cdp.Call(state.ctx, state.sessionID, "Page.stopScreencast", nil, nil); err != nil {
		return err
	}
	return v.startScreencastLocked(state)
}

func (v *ViewerController) dispatchInput(state *viewerSession, input browserstream.Control) error {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	params := map[string]any{"modifiers": input.Modifiers}
	method := ""
	switch input.Kind {
	case "pointerMove", "pointerDown", "pointerUp", "doubleClick":
		if input.X < 0 || input.Y < 0 || input.X > float64(state.width) || input.Y > float64(state.height) {
			return errors.New("pointer coordinates are outside the viewport")
		}
		method = "Input.dispatchMouseEvent"
		params["type"] = map[string]string{
			"pointerMove": "mouseMoved", "pointerDown": "mousePressed",
			"pointerUp": "mouseReleased", "doubleClick": "mousePressed",
		}[input.Kind]
		params["x"], params["y"] = input.X, input.Y
		params["button"] = normalizeMouseButton(input.Button)
		params["buttons"] = input.Buttons
		if input.Kind == "doubleClick" {
			params["clickCount"] = 2
		} else if input.ClickCount > 0 {
			params["clickCount"] = input.ClickCount
		}
	case "wheel":
		method = "Input.dispatchMouseEvent"
		params["type"], params["x"], params["y"] = "mouseWheel", input.X, input.Y
		params["deltaX"], params["deltaY"] = finite(input.DeltaX), finite(input.DeltaY)
	case "keyDown", "keyUp":
		if len(input.Key) > 128 || len(input.CodeValue) > 128 || len(input.Text) > 8<<10 {
			return errors.New("keyboard input exceeds its limit")
		}
		method = "Input.dispatchKeyEvent"
		params["type"], params["key"], params["code"] = input.Kind, input.Key, input.CodeValue
		if input.Text != "" {
			params["text"] = input.Text
		}
	case "text", "compositionCommit":
		if len(input.Text) > 8<<10 {
			return errors.New("text input exceeds its limit")
		}
		method = "Input.insertText"
		params = map[string]any{"text": input.Text}
	case "compositionStart", "compositionUpdate", "compositionCancel":
		if len(input.Text) > 8<<10 {
			return errors.New("composition input exceeds its limit")
		}
		method = "Input.imeSetComposition"
		if input.Kind == "compositionCancel" {
			input.Text = ""
		}
		params = map[string]any{"text": input.Text, "selectionStart": len([]rune(input.Text)), "selectionEnd": len([]rune(input.Text))}
	default:
		return errors.New("unsupported browser input kind")
	}
	if input.Kind != "doubleClick" {
		return state.cdp.Call(state.ctx, state.sessionID, method, params, nil)
	}
	if err := state.cdp.Call(state.ctx, state.sessionID, method, params, nil); err != nil {
		return err
	}
	params["type"] = "mouseReleased"
	return state.cdp.Call(state.ctx, state.sessionID, method, params, nil)
}

func (v *ViewerController) navigate(state *viewerSession, control browserstream.Control) error {
	state.opMu.Lock()
	defer state.opMu.Unlock()
	switch control.Operation {
	case "open":
		normalized, err := NormalizeAgentBrowserURL(control.URL)
		if err != nil {
			return err
		}
		return state.cdp.Call(state.ctx, state.sessionID, "Page.navigate", map[string]any{"url": normalized}, nil)
	case "reload":
		return state.cdp.Call(state.ctx, state.sessionID, "Page.reload", nil, nil)
	case "back", "forward":
		var history struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID int `json:"id"`
			} `json:"entries"`
		}
		if err := state.cdp.Call(state.ctx, state.sessionID, "Page.getNavigationHistory", nil, &history); err != nil {
			return err
		}
		index := history.CurrentIndex - 1
		if control.Operation == "forward" {
			index = history.CurrentIndex + 1
		}
		if index < 0 || index >= len(history.Entries) {
			return errors.New("browser navigation history has no matching entry")
		}
		return state.cdp.Call(state.ctx, state.sessionID, "Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[index].ID}, nil)
	default:
		return errors.New("unsupported browser navigation operation")
	}
}

func (v *ViewerController) tabOperation(state *viewerSession, control browserstream.Control) error {
	switch control.Operation {
	case "select":
		if control.TabID == "" {
			return errors.New("tab id is required")
		}
		if v.opts.Engine != nil {
			if _, err := v.opts.Engine.Execute(state.ctx, "tab-select", map[string]any{"tabId": control.TabID}); err != nil {
				return err
			}
		} else if err := state.cdp.Call(state.ctx, "", "Target.activateTarget", map[string]any{"targetId": control.TabID}, nil); err != nil {
			return err
		}
		if err := v.switchTarget(state, control.TabID); err != nil {
			return err
		}
	case "new":
		targetURL := "about:blank"
		if strings.TrimSpace(control.URL) != "" {
			normalized, err := NormalizeAgentBrowserURL(control.URL)
			if err != nil {
				return err
			}
			targetURL = normalized
		}
		targetID := ""
		if v.opts.Engine != nil {
			args := map[string]any{}
			if targetURL != "about:blank" {
				args["url"] = targetURL
			}
			if _, err := v.opts.Engine.Execute(state.ctx, "tab-new", args); err != nil {
				return err
			}
			var err error
			targetID, err = v.engineActiveTabID(state.ctx)
			if err != nil {
				return err
			}
		} else {
			var created struct {
				TargetID string `json:"targetId"`
			}
			if err := state.cdp.Call(state.ctx, "", "Target.createTarget", map[string]any{"url": targetURL}, &created); err != nil {
				return err
			}
			targetID = created.TargetID
		}
		if targetID == "" {
			return errors.New("browser engine did not report an active tab")
		}
		if err := v.switchTarget(state, targetID); err != nil {
			return err
		}
	case "close":
		targetID := control.TabID
		if targetID == "" {
			targetID = state.targetID
		}
		if v.opts.Engine != nil {
			args := map[string]any{}
			if control.TabID != "" {
				args["tabId"] = control.TabID
			}
			if _, err := v.opts.Engine.Execute(state.ctx, "tab-close", args); err != nil {
				return err
			}
			activeID, err := v.engineActiveTabID(state.ctx)
			if err != nil {
				return err
			}
			if activeID != "" {
				if err := v.switchTarget(state, activeID); err != nil {
					return err
				}
			}
		} else {
			if err := state.cdp.Call(state.ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID}, nil); err != nil {
				return err
			}
			tabs, err := v.listTargets(state)
			if err != nil {
				return err
			}
			if len(tabs) > 0 {
				if err := v.switchTarget(state, tabs[0].ID); err != nil {
					return err
				}
			}
		}
	default:
		return errors.New("unsupported browser tab operation")
	}
	v.publishState(state)
	return nil
}

func (v *ViewerController) engineActiveTabID(ctx context.Context) (string, error) {
	data, err := v.opts.Engine.Execute(ctx, "tabs", nil)
	if err != nil {
		return "", err
	}
	rawTabs, _ := data["tabs"].([]any)
	for _, raw := range rawTabs {
		tab, _ := raw.(map[string]any)
		active, _ := tab["active"].(bool)
		if active {
			return stringField(tab["tabId"]), nil
		}
	}
	return "", nil
}

func (v *ViewerController) dialogOperation(state *viewerSession, control browserstream.Control) error {
	accept := false
	switch control.Operation {
	case "accept":
		accept = true
	case "dismiss":
	default:
		return errors.New("unsupported browser dialog operation")
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	params := map[string]any{"accept": accept}
	if accept && control.Text != "" {
		if len(control.Text) > 8<<10 {
			return errors.New("dialog text exceeds its limit")
		}
		params["promptText"] = control.Text
	}
	return state.cdp.Call(state.ctx, state.sessionID, "Page.handleJavaScriptDialog", params, nil)
}

func (v *ViewerController) listTargets(state *viewerSession) ([]browserstream.Tab, error) {
	state.opMu.Lock()
	activeTarget := state.targetID
	state.opMu.Unlock()
	var response struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := state.cdp.Call(state.ctx, "", "Target.getTargets", nil, &response); err != nil {
		return nil, err
	}
	tabs := make([]browserstream.Tab, 0, len(response.TargetInfos))
	for _, target := range response.TargetInfos {
		if target.Type != "page" || target.TargetID == "" || strings.HasPrefix(target.URL, "devtools:") {
			continue
		}
		tabs = append(tabs, browserstream.Tab{
			ID: target.TargetID, URL: SanitizeBrowserURL(target.URL),
			Title: SanitizeBrowserTitle(target.Title), Active: target.TargetID == activeTarget,
		})
	}
	return tabs, nil
}

func (v *ViewerController) publishState(state *viewerSession) {
	tabs, err := v.listTargets(state)
	if err != nil {
		return
	}
	state.opMu.Lock()
	active := state.targetID
	sessionID := state.sessionID
	width, height := state.width, state.height
	loading := state.loading
	quality, fps := state.quality, state.fps
	dialogOpen := state.dialogOpen
	dialogType, dialogText, dialogPrompt := state.dialogType, state.dialogText, state.dialogPrompt
	state.opMu.Unlock()
	currentURL, currentTitle := "", ""
	for index := range tabs {
		tabs[index].Active = tabs[index].ID == active
		if tabs[index].Active {
			currentURL, currentTitle = tabs[index].URL, tabs[index].Title
		}
	}
	canGoBack, canGoForward := false, false
	var history struct {
		CurrentIndex int        `json:"currentIndex"`
		Entries      []struct{} `json:"entries"`
	}
	if sessionID != "" && state.cdp.Call(state.ctx, sessionID, "Page.getNavigationHistory", nil, &history) == nil {
		canGoBack = history.CurrentIndex > 0
		canGoForward = history.CurrentIndex >= 0 && history.CurrentIndex+1 < len(history.Entries)
	}
	v.enqueue(state, browserstream.Control{
		Type: "state", Version: browserstream.Version, StreamEpoch: state.epoch,
		TargetID: active, ActiveTabID: active, URL: currentURL, Title: currentTitle,
		Width: width, Height: height, Tabs: tabs, Owner: string(v.opts.Arbiter.Owner()),
		CanGoBack: canGoBack, CanGoForward: canGoForward, IsLoading: loading,
		DialogOpen: dialogOpen, DialogType: dialogType, DialogText: dialogText, DialogPrompt: dialogPrompt,
		Quality: quality, FPS: fps,
	})
}

func (v *ViewerController) writeControls(conn *websocket.Conn, state *viewerSession) error {
	for {
		select {
		case <-state.ctx.Done():
			return state.ctx.Err()
		case control := <-state.control:
			payload, err := json.Marshal(control)
			if err != nil {
				return err
			}
			state.writeMu.Lock()
			err = conn.Write(state.ctx, websocket.MessageText, payload)
			state.writeMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

func (v *ViewerController) writeFrames(conn *websocket.Conn, state *viewerSession) error {
	for {
		frame, ok := state.frames.Next(state.ctx)
		if !ok {
			return state.ctx.Err()
		}
		state.writeMu.Lock()
		err := conn.Write(state.ctx, websocket.MessageBinary, frame)
		state.writeMu.Unlock()
		if err != nil {
			return err
		}
		state.framesSent.Add(1)
		state.bytesSent.Add(uint64(len(frame)))
	}
}

func (v *ViewerController) enqueue(state *viewerSession, control browserstream.Control) {
	select {
	case state.control <- control:
	case <-state.ctx.Done():
	default:
		state.cancel()
	}
}

func (v *ViewerController) AgentActionStarted(action string) {
	v.mu.Lock()
	state := v.session
	v.mu.Unlock()
	if state == nil {
		return
	}
	state.opMu.Lock()
	minFrame := state.sequence + 1
	state.opMu.Unlock()
	v.enqueue(state, browserstream.Control{
		Type: "agent_action", Version: browserstream.Version, StreamEpoch: state.epoch,
		Kind: action, Owner: string(ControlAgent), MinFrameSeq: minFrame, Running: true,
	})
}

func (v *ViewerController) AgentActionFinished(action string, args map[string]any, result map[string]any) {
	v.mu.Lock()
	state := v.session
	v.mu.Unlock()
	if state == nil {
		return
	}
	targetID := ""
	switch action {
	case "tab-select":
		targetID, _ = args["tabId"].(string)
	case "tab-new":
		targetID, _ = result["id"].(string)
	case "tab-close":
		targetID, _ = result["activeTabId"].(string)
	}
	state.opMu.Lock()
	activeTarget := state.targetID
	state.opMu.Unlock()
	if targetID != "" && targetID != activeTarget {
		if err := v.switchTarget(state, targetID); err != nil {
			v.opts.Logger.Debug("sync browser viewer target after agent action", "error", err, "action", action)
		}
	}
	v.publishState(state)
	v.enqueue(state, browserstream.Control{
		Type: "agent_action", Version: browserstream.Version, StreamEpoch: state.epoch,
		Kind: action, Owner: string(v.opts.Arbiter.Owner()), Running: false,
	})
}

func normalizeMouseButton(button string) string {
	switch button {
	case "left", "middle", "right", "back", "forward":
		return button
	default:
		return "none"
	}
}

func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func allowViewerInput(state *viewerSession, now time.Time) bool {
	state.rateMu.Lock()
	defer state.rateMu.Unlock()
	if state.rateStart.IsZero() || now.Sub(state.rateStart) >= time.Second {
		state.rateStart = now
		state.rateCount = 0
	}
	if state.rateCount >= 120 {
		return false
	}
	state.rateCount++
	return true
}
