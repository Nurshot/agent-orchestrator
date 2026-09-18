package gooseacp

import (
	"context"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureStartsACPSubcommand(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestConfigureMapsPermissionModesToGooseMode(t *testing.T) {
	tests := map[ports.PermissionMode]string{
		ports.PermissionModeDefault:           "",
		ports.PermissionModeAcceptEdits:       "smart_approve",
		ports.PermissionModeAuto:              "auto",
		ports.PermissionModeBypassPermissions: "auto",
	}
	for mode, want := range tests {
		args, env, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		if wantArgs := []string{"acp"}; !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("mode %q: args = %#v, want %#v", mode, args, wantArgs)
		}
		if want == "" {
			if env != nil {
				t.Fatalf("mode %q: env = %#v, want nil", mode, env)
			}
			continue
		}
		if env["GOOSE_MODE"] != want {
			t.Fatalf("mode %q: GOOSE_MODE = %q, want %q", mode, env["GOOSE_MODE"], want)
		}
	}
}

func TestValidateTurnSettingsRejectsApprovalChanges(t *testing.T) {
	if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{}); err != nil {
		t.Fatalf("empty approval should be accepted: %v", err)
	}
	if err := validateTurnSettings(ports.PermissionModeAuto, ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}); err != nil {
		t.Fatalf("same approval should be accepted: %v", err)
	}
	if err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}); err == nil {
		t.Fatal("changed approval should be rejected")
	}
}
