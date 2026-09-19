package acp

import (
	"errors"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func offered(kinds ...acpsdk.PermissionOptionKind) []acpsdk.PermissionOption {
	options := make([]acpsdk.PermissionOption, 0, len(kinds))
	for _, kind := range kinds {
		options = append(options, acpsdk.PermissionOption{
			OptionId: acpsdk.PermissionOptionId(kind), Kind: kind,
		})
	}
	return options
}

func request(kind *acpsdk.ToolKind, options []acpsdk.PermissionOption) acpsdk.RequestPermissionRequest {
	return acpsdk.RequestPermissionRequest{
		ToolCall: acpsdk.ToolCallUpdate{Kind: kind}, Options: options,
	}
}

// The three shapes in use: a provider that settles auto and bypass at launch
// keeps them out of the policy (Cline), one that offers no persistent auto mode
// blanket-allows only bypass (Auggie, Cursor), and one that resolves everything
// over ACP blanket-allows both (Kiro, Autohand).
func TestStandardPermissionPolicyResolvesPerBindingModes(t *testing.T) {
	edit, execute := acpsdk.ToolKindEdit, acpsdk.ToolKindExecute
	all := offered(
		acpsdk.PermissionOptionKindAllowOnce,
		acpsdk.PermissionOptionKindAllowAlways,
		acpsdk.PermissionOptionKindRejectOnce,
	)
	tests := []struct {
		name         string
		blanketAllow []ports.PermissionMode
		mode         ports.PermissionMode
		kind         *acpsdk.ToolKind
		wantID       acpsdk.PermissionOptionId
		wantHandled  bool
	}{
		{name: "default always parks", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits allows an edit once", mode: ports.PermissionModeAcceptEdits, kind: &edit,
			wantID: "allow_once", wantHandled: true},
		{name: "accept edits parks a command", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		{name: "accept edits parks an unkinded call", mode: ports.PermissionModeAcceptEdits, kind: nil},
		{name: "launch-settled auto parks", mode: ports.PermissionModeAuto, kind: &execute},
		{name: "bypass-only binding parks auto",
			blanketAllow: []ports.PermissionMode{ports.PermissionModeBypassPermissions},
			mode:         ports.PermissionModeAuto, kind: &execute},
		{name: "bypass-only binding allows bypass persistently",
			blanketAllow: []ports.PermissionMode{ports.PermissionModeBypassPermissions},
			mode:         ports.PermissionModeBypassPermissions, kind: &execute,
			wantID: "allow_always", wantHandled: true},
		{name: "full binding allows auto persistently",
			blanketAllow: []ports.PermissionMode{ports.PermissionModeAuto, ports.PermissionModeBypassPermissions},
			mode:         ports.PermissionModeAuto, kind: &execute,
			wantID: "allow_always", wantHandled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, handled := StandardPermissionPolicy(tt.blanketAllow...)(tt.mode, request(tt.kind, all))
			if id != tt.wantID || handled != tt.wantHandled {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", id, handled, tt.wantID, tt.wantHandled)
			}
		})
	}
}

// A provider that offers no persistent choice must still be auto-answered,
// otherwise a bypass session parks on every tool call.
func TestStandardPermissionPolicyFallsBackToAllowOnce(t *testing.T) {
	execute := acpsdk.ToolKindExecute
	policy := StandardPermissionPolicy(ports.PermissionModeBypassPermissions)
	id, handled := policy(ports.PermissionModeBypassPermissions,
		request(&execute, offered(acpsdk.PermissionOptionKindAllowOnce)))
	if !handled || id != "allow_once" {
		t.Fatalf("selection = (%q, %v), want (allow_once, true)", id, handled)
	}
}

func TestPermissionOptionReportsMissingKind(t *testing.T) {
	options := offered(acpsdk.PermissionOptionKindRejectOnce)
	if id, ok := PermissionOption(options, acpsdk.PermissionOptionKindAllowAlways); ok {
		t.Fatalf("selection = %q, want no match", id)
	}
	if id, ok := PermissionOption(nil, acpsdk.PermissionOptionKindAllowOnce); ok {
		t.Fatalf("selection from no options = %q, want no match", id)
	}
}

func TestApprovalFixedAtLaunchRejectsOnlyRealChanges(t *testing.T) {
	validate := ApprovalFixedAtLaunch("Kiro ACP approval mode")
	if err := validate(ports.PermissionModeDefault, ports.ChatTurnSettings{}); err != nil {
		t.Fatalf("unset approval: %v", err)
	}
	if err := validate(ports.PermissionModeAuto,
		ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}); err != nil {
		t.Fatalf("unchanged approval: %v", err)
	}
	err := validate(ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeAuto})
	if !errors.Is(err, ErrACPSetterUnsupported) {
		t.Fatalf("err = %v, want ErrACPSetterUnsupported", err)
	}
	if !strings.Contains(err.Error(), "Kiro ACP approval mode") {
		t.Fatalf("err = %q, want the binding's surface named", err)
	}
}
