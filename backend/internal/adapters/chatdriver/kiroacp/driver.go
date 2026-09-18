// Package kiroacp binds the user's own Kiro (AWS) installation to AO's reusable
// ACP Chat transport.
//
// Kiro exposes ACP natively via `kiro-cli acp`; AO launches the exact binary
// resolved by the existing Kiro agent plugin and selects the same workspace-local
// custom agent the TUI path uses, so login, models, steering, and MCP
// configuration remain the user's own.
package kiroacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kiro"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `kiro-cli acp --agent ao` from the exact binary resolved by the
// existing Kiro agent plugin. The custom agent carries AO's standing
// instructions (installed during workspace preparation) and Kiro owns models,
// steering, and auth. Kiro's ACP server advertises session/set_model and
// session/load, so models and native history are available to Chat.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessKiro,
		Configure:            configure,
		PermissionPolicy:     permissionPolicy,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

// configure builds the `kiro-cli acp` argv. AO selects the workspace-local
// custom agent by name; its system prompt is written by the Kiro agent plugin's
// GetAgentHooks, so configure forwards no separate system prompt. Kiro's
// tool-trust flags are documented for `chat` rather than `acp`, so AO resolves
// permissions through ACP requests instead of guessing a launch flag.
func configure(_ context.Context, _ acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return []string{"acp", "--agent", kiro.AgentName}, nil, nil
}

// permissionPolicy resolves Kiro's exact offered permission choices for AO's
// stronger modes before the generic client parks a request for a human.
// accept-edits allows file-changing tools once; auto and bypass allow every
// tool. Default keeps the ordinary approval flow.
func permissionPolicy(
	mode ports.PermissionMode,
	params acpsdk.RequestPermissionRequest,
) (acpsdk.PermissionOptionId, bool) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		kind := acpsdk.ToolKind("")
		if params.ToolCall.Kind != nil {
			kind = *params.ToolCall.Kind
		}
		if kind != acpsdk.ToolKindEdit && kind != acpsdk.ToolKindDelete && kind != acpsdk.ToolKindMove {
			return "", false
		}
		return permissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		if id, ok := permissionOption(params.Options, acpsdk.PermissionOptionKindAllowAlways); ok {
			return id, true
		}
		return permissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
	default:
		return "", false
	}
}

func permissionOption(
	options []acpsdk.PermissionOption,
	kind acpsdk.PermissionOptionKind,
) (acpsdk.PermissionOptionId, bool) {
	for _, option := range options {
		if option.Kind == kind {
			return option.OptionId, true
		}
	}
	return "", false
}

// sessionOptions maps AO's durable model choice onto Kiro's advertised
// session/set_model selector.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}

// validateTurnSettings rejects approval changes a Kiro ACP session cannot make.
// AO's launch does not fix a Kiro trust flag, so a session's effective mode can
// only change through a new Chat launch; mid-turn approval changes are refused
// rather than silently ignored.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	if ports.NormalizePermissionMode(settings.Approval) == ports.NormalizePermissionMode(initial) {
		return nil
	}
	return fmt.Errorf(
		"%w: Kiro ACP approval mode is fixed at process launch (%s); restart Chat to run it in %s",
		acpdriver.ErrACPSetterUnsupported,
		ports.NormalizePermissionMode(initial), ports.NormalizePermissionMode(settings.Approval))
}
