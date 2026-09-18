package autohandacp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSiblingAdapterResolvesExecutableBesideAutohand(t *testing.T) {
	dir := t.TempDir()
	autohand := filepath.Join(dir, binaryName("autohand"))
	adapter := filepath.Join(dir, binaryName("autohand-acp"))
	writeExecutable(t, autohand)
	writeExecutable(t, adapter)

	if got := siblingAdapter(autohand); got != adapter {
		t.Fatalf("siblingAdapter = %q, want %q", got, adapter)
	}
}

func TestSiblingAdapterReturnsEmptyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	autohand := filepath.Join(dir, binaryName("autohand"))
	writeExecutable(t, autohand)

	if got := siblingAdapter(autohand); got != "" {
		t.Fatalf("siblingAdapter = %q, want empty", got)
	}
}

func TestResolveAdapterBinaryFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	autohand := filepath.Join(dir, binaryName("autohand"))
	writeExecutable(t, autohand)
	pathDir := t.TempDir()
	adapter := filepath.Join(pathDir, binaryName("autohand-acp"))
	writeExecutable(t, adapter)
	t.Setenv("PATH", pathDir)

	got, err := resolveAdapterBinary(context.Background(), fixedPlugin(autohand))
	if err != nil {
		t.Fatalf("resolveAdapterBinary: %v", err)
	}
	if got != adapter {
		t.Fatalf("resolved = %q, want %q", got, adapter)
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
		{name: "auto prefers persistent allow", mode: ports.PermissionModeAuto, kind: &execute, wantID: "allow-always", handled: true},
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

type fixedPlugin string

func (p fixedPlugin) ResolveBinary(context.Context) (string, error) { return string(p), nil }
func (fixedPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}

func binaryName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
