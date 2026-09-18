// Package kilocodeacp binds the user's own Kilo Code CLI installation to AO's
// reusable ACP Chat transport.
//
// The Kilo Code CLI (@kilocode/cli, binaries `kilo`/`kilocode`) is a fork of
// sst/opencode and exposes ACP through its native `acp` subcommand. AO launches
// the exact binary resolved by the existing Kilo Code agent plugin and reuses
// its KILO_CONFIG_CONTENT inline-config surface for standing instructions and
// permission modes.
package kilocodeacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kilocode"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `kilocode acp` from the exact binary resolved by the existing
// Kilo Code agent plugin. Models and modes come from the live ACP session
// advertisements; Kilo Code owns tools, providers, and auth.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessKilocode,
		Configure:            configure,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

// configure builds the `kilocode acp` argv. When AO carries standing
// instructions or a non-default permission mode, they ride on
// KILO_CONFIG_CONTENT, the same highest-precedence inline config the TUI path
// uses. Kilo's `acp` subcommand accepts no model flag, so the model is applied
// through session/set_model instead.
func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"acp"}
	if strings.TrimSpace(cfg.SystemPrompt) == "" &&
		ports.NormalizePermissionMode(cfg.Permissions) == ports.PermissionModeDefault {
		return args, nil, nil
	}
	content, err := kilocode.PrepareACPConfigContent(
		cfg.Env["KILO_CONFIG_CONTENT"], cfg.SystemPrompt, string(cfg.SessionID), cfg.Permissions)
	if err != nil {
		return nil, nil, err
	}
	return args, map[string]string{"KILO_CONFIG_CONTENT": content}, nil
}

// sessionOptions maps AO's durable model choice onto Kilo Code's advertised
// "model" config option.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}

// validateTurnSettings rejects approval changes a Kilo Code ACP session cannot
// make. The permission map is fixed by the launch-time KILO_CONFIG_CONTENT, so a
// change requires restarting Chat.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	if ports.NormalizePermissionMode(settings.Approval) == ports.NormalizePermissionMode(initial) {
		return nil
	}
	return fmt.Errorf(
		"%w: Kilo Code ACP permission mode is fixed at process launch (%s); restart Chat to run it in %s",
		acpdriver.ErrACPSetterUnsupported,
		ports.NormalizePermissionMode(initial), ports.NormalizePermissionMode(settings.Approval))
}
