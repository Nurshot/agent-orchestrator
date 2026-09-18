// Package vibeacp binds the user's own Mistral Vibe installation to AO's
// reusable ACP Chat transport.
//
// Vibe ships its ACP server as a separate `vibe-acp` executable rather than an
// `acp` subcommand of `vibe`. AO launches that exact user-installed binary and
// keeps the existing Vibe agent plugin as the canonical auth probe; login,
// providers, models, agents, and MCP configuration remain the user's own.
package vibeacp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// vibePlugin is the subset of AO's existing Vibe agent plugin the Chat driver
// reuses for binary resolution and local auth probing.
type vibePlugin interface {
	ResolveBinary(context.Context) (string, error)
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

// New constructs Vibe's Chat driver over the existing Vibe agent plugin.
func New(plugin vibePlugin, log *slog.Logger) ports.ChatDriver {
	return acpdriver.New(acpdriver.Config{
		Harness: domain.HarnessVibe,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityTools:     true,
			ports.ChatCapabilityApprovals: true,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
		},
		Probe: func(ctx context.Context) error {
			if _, err := resolveVibeACPBinary(ctx, plugin); err != nil {
				return fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			status, err := plugin.AuthStatus(ctx)
			if err == nil && status == ports.AgentAuthStatusUnauthorized {
				return ports.ErrChatAuthRequired
			}
			if err != nil && log != nil {
				log.Debug("Vibe auth probe inconclusive; continuing", "error", err)
			}
			return nil
		},
		Launch: func(ctx context.Context, cfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			binary, err := resolveVibeACPBinary(ctx, plugin)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			env := make(map[string]string, len(cfg.Env))
			for key, value := range cfg.Env {
				env[key] = value
			}
			return acpdriver.Launch{Command: binary, Env: env}, nil
		},
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

// resolveVibeACPBinary finds the `vibe-acp` executable. Vibe's one-line and uv
// installers place it beside `vibe`, so the sibling of the plugin-resolved
// binary is preferred; PATH and common user install locations are the fallback.
func resolveVibeACPBinary(ctx context.Context, plugin vibePlugin) (string, error) {
	vibeBinary, err := plugin.ResolveBinary(ctx)
	if err != nil {
		return "", err
	}
	if sibling := siblingVibeACP(vibeBinary); sibling != "" {
		return sibling, nil
	}
	return binaryutil.ResolveBinary(ctx, vibeACPSpec)
}

func siblingVibeACP(vibeBinary string) string {
	dir := filepath.Dir(vibeBinary)
	candidates := []string{"vibe-acp"}
	if runtime.GOOS == "windows" {
		candidates = []string{"vibe-acp.exe", "vibe-acp.cmd", "vibe-acp"}
	}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

var vibeACPSpec = binaryutil.BinarySpec{
	Label:         "vibe-acp",
	Names:         []string{"vibe-acp"},
	WinNames:      []string{"vibe-acp.exe", "vibe-acp.cmd", "vibe-acp"},
	UnixPaths:     []string{"/usr/local/bin/vibe-acp", "/opt/homebrew/bin/vibe-acp"},
	UnixHomePaths: [][]string{{".local", "bin", "vibe-acp"}},
}

// validateTurnSettings rejects approval changes a Vibe ACP session cannot make.
// `vibe-acp` accepts no approval flag and exposes no string-valued approval
// config option, so a change requires restarting Chat.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	if ports.NormalizePermissionMode(settings.Approval) == ports.NormalizePermissionMode(initial) {
		return nil
	}
	return fmt.Errorf(
		"%w: Vibe ACP approval mode is fixed at process launch (%s); restart Chat to run it in %s",
		acpdriver.ErrACPSetterUnsupported,
		ports.NormalizePermissionMode(initial), ports.NormalizePermissionMode(settings.Approval))
}
