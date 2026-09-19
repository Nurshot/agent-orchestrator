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
			return acpdriver.Launch{Command: binary, Env: cfg.Env}, nil
		},
		ValidateTurnSettings: acpdriver.ApprovalFixedAtLaunch("Vibe ACP approval mode"),
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
	if sibling := binaryutil.SiblingBinary(vibeBinary, vibeACPSpec); sibling != "" {
		return sibling, nil
	}
	return binaryutil.ResolveBinary(ctx, vibeACPSpec)
}

var vibeACPSpec = binaryutil.BinarySpec{
	Label:         "vibe-acp",
	Names:         []string{"vibe-acp"},
	WinNames:      []string{"vibe-acp.exe", "vibe-acp.cmd", "vibe-acp"},
	UnixPaths:     []string{"/usr/local/bin/vibe-acp", "/opt/homebrew/bin/vibe-acp"},
	UnixHomePaths: [][]string{{".local", "bin", "vibe-acp"}},
}
