package auggieacp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureStartsACPMode(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestConfigurePassesModel(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Model: "claude-sonnet-4"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp", "--model", "claude-sonnet-4"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureWritesStandingRulesFile(t *testing.T) {
	dataDir := t.TempDir()
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-1", DataDir: dataDir, SystemPrompt: "Follow AO worker rules.",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	wantPath := filepath.Join(dataDir, "prompts", "worker-1", "auggie-acp", "ao-standing-rules.md")
	want := []string{"--acp", "--rules", wantPath}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read standing rules: %v", err)
	}
	if got := string(data); got != "Follow AO worker rules.\n" {
		t.Fatalf("standing rules = %q", got)
	}
}

func TestConfigureOmitsBlankSystemPrompt(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{SystemPrompt: "   "})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureRequiresDataDirForStandingRules(t *testing.T) {
	_, _, err := configure(context.Background(), acpdriver.LaunchConfig{SystemPrompt: "Follow AO rules."})
	if err == nil {
		t.Fatal("configure succeeded without AO data directory")
	}
	if !strings.Contains(err.Error(), "data directory") {
		t.Fatalf("error = %v, want data directory guidance", err)
	}
}

func TestPermissionPolicyResolvesAdvertisedModes(t *testing.T) {
	edit := acpsdk.ToolKindEdit
	execute := acpsdk.ToolKindExecute
	options := []acpsdk.PermissionOption{
		{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "allow-always", Kind: acpsdk.PermissionOptionKindAllowAlways},
		{OptionId: "reject-once", Kind: acpsdk.PermissionOptionKindRejectOnce},
	}
	tests := []struct {
		name    string
		mode    ports.PermissionMode
		kind    *acpsdk.ToolKind
		wantID  acpsdk.PermissionOptionId
		handled bool
	}{
		{name: "default parks", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits allows edit once", mode: ports.PermissionModeAcceptEdits, kind: &edit, wantID: "allow-once", handled: true},
		{name: "accept edits parks execute", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		{name: "auto parks", mode: ports.PermissionModeAuto, kind: &execute},
		{name: "bypass prefers persistent allow", mode: ports.PermissionModeBypassPermissions, kind: &execute, wantID: "allow-always", handled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotHandled := permissionPolicy(tt.mode, acpsdk.RequestPermissionRequest{
				ToolCall: acpsdk.ToolCallUpdate{Kind: tt.kind}, Options: options,
			})
			if gotID != tt.wantID || gotHandled != tt.handled {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", gotID, gotHandled, tt.wantID, tt.handled)
			}
		})
	}
}

func TestPermissionPolicyFallsBackToAllowOnceWithoutPersistentOption(t *testing.T) {
	execute := acpsdk.ToolKindExecute
	options := []acpsdk.PermissionOption{
		{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
	}
	gotID, handled := permissionPolicy(ports.PermissionModeBypassPermissions, acpsdk.RequestPermissionRequest{
		ToolCall: acpsdk.ToolCallUpdate{Kind: &execute}, Options: options,
	})
	if !handled || gotID != "allow-once" {
		t.Fatalf("selection = (%q, %v), want (allow-once, true)", gotID, handled)
	}
}

func TestSessionOptionsUseModelConfigOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); len(got) != 0 {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "claude-sonnet-4"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "claude-sonnet-4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}
