// Package opencodeacp binds the user's own OpenCode installation to AO's
// reusable ACP Chat transport.
package opencodeacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `opencode acp` from the exact binary resolved by the existing
// OpenCode agent plugin. AO adds only a per-session inline overlay for its
// standing instructions; permissions are resolved per request.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessOpenCode,
		Configure:            configure,
		SessionOptions:       sessionOptions,
		PermissionPolicy:     permissionPolicy,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	existing := cfg.Env["OPENCODE_CONFIG_CONTENT"]
	content, err := opencode.PrepareACPConfigContent(existing, cfg.SystemPrompt, string(cfg.SessionID))
	if err != nil {
		return nil, nil, err
	}
	if content == existing {
		return []string{"acp"}, nil, nil
	}
	return []string{"acp"}, map[string]string{"OPENCODE_CONFIG_CONTENT": content}, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	// OpenCode advertises effort as id "effort" (category "thought_level") with
	// the model's variant names as values. The model setter resets the variant
	// to "default" else the first variant when no explicit variant is given,
	// so the model must be applied first and the effort second.
	if settings.Effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "effort", Value: settings.Effort})
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

// OpenCode parses model overrides as provider/model. A provider display name is
// not an alias for its configured default model; keep that default only when
// the override is empty. Leave model availability to the user's OpenCode.
func validateTurnSettings(_ ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Model == "" {
		return nil
	}
	provider, model, found := strings.Cut(settings.Model, "/")
	if !found || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return fmt.Errorf("%w: OpenCode model %q must use provider/model format (for example, anthropic/claude-sonnet); select a full model ID from `opencode models`, or clear the model override to use Agent default", ports.ErrChatConfigOptionInvalid, settings.Model)
	}
	return nil
}

// permissionPolicy is where every AO permission mode is realized for OpenCode.
// Unlike Claude, OpenCode advertises no permission catalog of its own — its
// `mode` option is build/plan — and a launch-time `permission` rule could not be
// taken back mid-session. Answering each request instead keeps all four modes
// switchable in both directions while a session runs, which is the behavior
// Claude gets from session/set_mode.
//
// The tiers key on the tool kinds OpenCode reports: bash and shell are execute,
// webfetch is fetch, edit/write/patch are edit, grep/glob are search, task is
// think. Everything it does not classify — MCP tools, skills — arrives as other
// and falls through to the user, so an unrecognized kind is never auto-allowed
// except under bypass.
func permissionPolicy(
	mode ports.PermissionMode,
	params acpsdk.RequestPermissionRequest,
) (acpsdk.PermissionOptionId, bool) {
	var kind acpsdk.ToolKind
	if params.ToolCall.Kind != nil {
		kind = *params.ToolCall.Kind
	}
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeBypassPermissions:
		return allowOption(params.Options,
			acpsdk.PermissionOptionKindAllowAlways, acpsdk.PermissionOptionKindAllowOnce)
	case ports.PermissionModeAuto:
		if !autoAllowedKinds[kind] {
			return "", false
		}
	case ports.PermissionModeAcceptEdits:
		if !editKinds[kind] {
			return "", false
		}
	default:
		return "", false
	}
	return allowOption(params.Options,
		acpsdk.PermissionOptionKindAllowOnce, acpsdk.PermissionOptionKindAllowAlways)
}

var editKinds = map[acpsdk.ToolKind]bool{
	acpsdk.ToolKindEdit: true, acpsdk.ToolKindDelete: true, acpsdk.ToolKindMove: true,
}

// Auto allows the read/write work of a turn and leaves shell commands, and any
// kind AO cannot classify, to the user.
var autoAllowedKinds = map[acpsdk.ToolKind]bool{
	acpsdk.ToolKindEdit: true, acpsdk.ToolKindDelete: true, acpsdk.ToolKindMove: true,
	acpsdk.ToolKindRead: true, acpsdk.ToolKindSearch: true,
	acpsdk.ToolKindFetch: true, acpsdk.ToolKindThink: true,
}

func allowOption(
	options []acpsdk.PermissionOption,
	preferred ...acpsdk.PermissionOptionKind,
) (acpsdk.PermissionOptionId, bool) {
	for _, kind := range preferred {
		for _, option := range options {
			if option.Kind == kind {
				return option.OptionId, true
			}
		}
	}
	return "", false
}
