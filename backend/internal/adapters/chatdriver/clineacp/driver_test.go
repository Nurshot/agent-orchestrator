package clineacp

import (
	"context"
	"reflect"
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

func TestConfigureEnablesAutoApproveForAutoAndBypass(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	} {
		args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		want := []string{"--acp", "--auto-approve", "true"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("mode %q: args = %#v, want %#v", mode, args, want)
		}
	}
}

func TestConfigureKeepsPromptingForDefaultAndAcceptEdits(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		ports.PermissionModeDefault,
		ports.PermissionModeAcceptEdits,
	} {
		args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		want := []string{"--acp"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("mode %q: args = %#v, want %#v", mode, args, want)
		}
	}
}

func TestConfigureDeliversModelThroughLaunchEnv(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{Model: "anthropic/claude-sonnet-4"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if want := []string{"--acp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	wantEnv := map[string]string{"CLINE_MODEL": "anthropic/claude-sonnet-4"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", env, wantEnv)
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
		{name: "auto parks at request time", mode: ports.PermissionModeAuto, kind: &edit},
		{name: "bypass parks at request time", mode: ports.PermissionModeBypassPermissions, kind: &execute},
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

func TestSessionOptionsUseModelConfigOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); len(got) != 0 {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "anthropic/claude-sonnet-4"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "anthropic/claude-sonnet-4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestValidateTurnSettingsRejectsApprovalChanges(t *testing.T) {
	if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{}); err != nil {
		t.Fatalf("empty approval should be accepted: %v", err)
	}
	if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeDefault}); err != nil {
		t.Fatalf("same approval should be accepted: %v", err)
	}
	if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}); err == nil {
		t.Fatal("changed approval should be rejected")
	}
}
