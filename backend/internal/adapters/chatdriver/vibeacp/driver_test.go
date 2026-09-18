package vibeacp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSiblingVibeACPResolvesExecutableBesideVibe(t *testing.T) {
	dir := t.TempDir()
	vibe := filepath.Join(dir, binaryName("vibe"))
	acp := filepath.Join(dir, binaryName("vibe-acp"))
	writeExecutable(t, vibe)
	writeExecutable(t, acp)

	if got := siblingVibeACP(vibe); got != acp {
		t.Fatalf("siblingVibeACP = %q, want %q", got, acp)
	}
}

func TestSiblingVibeACPReturnsEmptyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	vibe := filepath.Join(dir, binaryName("vibe"))
	writeExecutable(t, vibe)

	if got := siblingVibeACP(vibe); got != "" {
		t.Fatalf("siblingVibeACP = %q, want empty", got)
	}
}

func TestSiblingVibeACPIgnoresDirectory(t *testing.T) {
	dir := t.TempDir()
	vibe := filepath.Join(dir, binaryName("vibe"))
	writeExecutable(t, vibe)
	if err := os.Mkdir(filepath.Join(dir, binaryName("vibe-acp")), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := siblingVibeACP(vibe); got != "" {
		t.Fatalf("siblingVibeACP = %q, want empty for a directory", got)
	}
}

func TestResolveVibeACPBinaryFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	vibe := filepath.Join(dir, binaryName("vibe"))
	writeExecutable(t, vibe)
	pathDir := t.TempDir()
	acp := filepath.Join(pathDir, binaryName("vibe-acp"))
	writeExecutable(t, acp)
	t.Setenv("PATH", pathDir)

	got, err := resolveVibeACPBinary(context.Background(), fixedPlugin(vibe))
	if err != nil {
		t.Fatalf("resolveVibeACPBinary: %v", err)
	}
	if got != acp {
		t.Fatalf("resolved = %q, want %q", got, acp)
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
