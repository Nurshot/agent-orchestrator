//go:build darwin || linux

package unrealagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeProviderClient struct {
	request chan llm.Request
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(value []byte) (int, error) { return write(value) }

func (client *fakeProviderClient) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	client.request <- request
	return llm.Response{
		ID: "response-1",
		Output: []llm.Item{{
			ProviderID: "message-1",
			Type:       llm.ItemMessage,
			Data:       llm.Message{Role: llm.RoleAssistant, Text: "done"},
		}},
		Usage: llm.Usage{InputTokens: 3, OutputTokens: 1},
	}, nil
}

func (*fakeProviderClient) Close() error { return nil }

func TestProviderRunsPersistentTurnProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	client := &fakeProviderClient{request: make(chan llm.Request, 1)}
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}
	done := make(chan error, 1)
	go func() {
		done <- runProvider(ctx, cfg, inputReader, outputWriter, client, "test-model")
		_ = outputWriter.Close()
	}()

	decoder := json.NewDecoder(outputReader)
	var incoming frame
	if err := decoder.Decode(&incoming); err != nil || incoming.Type != "ready" {
		t.Fatalf("ready frame = (%#v, %v)", incoming, err)
	}
	if err := json.NewEncoder(inputWriter).Encode(command{
		Version: protocolVersion, Type: "turn", RequestID: "request-1",
		ProviderTurnID: "turn-1", MessageID: "message-1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	seen := map[ports.ChatEventKind]bool{}
	for !seen[ports.ChatEventTurnCompleted] {
		incoming = frame{}
		if err := decoder.Decode(&incoming); err != nil {
			t.Fatal(err)
		}
		if incoming.Type == "event" && incoming.Event != nil {
			seen[incoming.Event.Kind] = true
			if incoming.EventID == "" {
				t.Fatalf("event %#v has no durable id", incoming.Event)
			}
		}
	}
	for _, kind := range []ports.ChatEventKind{
		ports.ChatEventTurnStarted,
		ports.ChatEventMessageDelta,
		ports.ChatEventMessageCompleted,
		ports.ChatEventUsage,
		ports.ChatEventTurnCompleted,
	} {
		if !seen[kind] {
			t.Errorf("missing %s event", kind)
		}
	}
	select {
	case request := <-client.request:
		if request.Model.ID != "test-model" || !requestContainsUserText(request, "hello") {
			t.Errorf("model request = %#v", request)
		}
	default:
		t.Error("model was not called")
	}
	if err := json.NewEncoder(inputWriter).Encode(command{
		Version: protocolVersion, Type: "turn", RequestID: "request-2",
		ProviderTurnID: "turn-1", MessageID: "message-1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	for incoming.RequestID != "request-2" {
		incoming = frame{}
		if err := decoder.Decode(&incoming); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case duplicate := <-client.request:
		t.Fatalf("duplicate message called model again: %#v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}

	_ = inputWriter.Close()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.DataDir, "agent-runtime", string(domain.HarnessUnreal), "sessions", "*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("persisted Unreal session files = (%v, %v)", matches, err)
	}
}

func requestContainsUserText(request llm.Request, text string) bool {
	for _, item := range request.Input {
		if item.Type == llm.ItemMessage {
			message := item.Data.(llm.Message)
			if message.Role == llm.RoleUser && message.Text == text {
				return true
			}
		}
	}
	return false
}

func TestEventJournalReplaysUntilAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	firstOutput := new(bytes.Buffer)
	journal, err := newEventJournal(path, firstOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.emit(wireEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	var persisted frame
	if err := json.NewDecoder(firstOutput).Decode(&persisted); err != nil {
		t.Fatal(err)
	}

	replayOutput := new(bytes.Buffer)
	restarted, err := newEventJournal(path, replayOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.replay(); err != nil {
		t.Fatal(err)
	}
	var replayed frame
	if err := json.NewDecoder(replayOutput).Decode(&replayed); err != nil || replayed.EventID != persisted.EventID {
		t.Fatalf("replayed frame = (%#v, %v), want event %q", replayed, err, persisted.EventID)
	}
	if err := restarted.ack(replayed.EventID); err != nil {
		t.Fatal(err)
	}

	afterAck := new(bytes.Buffer)
	restarted, err = newEventJournal(path, afterAck)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.replay(); err != nil {
		t.Fatal(err)
	}
	if afterAck.Len() != 0 {
		t.Fatalf("acknowledged event replayed: %s", afterAck.String())
	}
}

func TestEventJournalRunsCompletionBeforePublishingEvent(t *testing.T) {
	completed := false
	journal, err := newEventJournal(filepath.Join(t.TempDir(), "events.json"), writerFunc(func(value []byte) (int, error) {
		if !completed {
			t.Error("event published before durable completion callback")
		}
		return len(value), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.emitAfterPersist(wireEvent{Kind: ports.ChatEventTurnCompleted}, func() error {
		completed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
