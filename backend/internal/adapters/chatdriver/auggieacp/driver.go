// Package auggieacp binds the user's own Auggie (Augment Code) installation to
// AO's reusable ACP Chat transport.
//
// Auggie exposes ACP natively via `auggie --acp`; AO launches the exact binary
// resolved by the existing Auggie agent plugin, so login, subscription, rules,
// MCP configuration, and settings remain the user's own. No path scrapes
// terminal output or packages a second provider CLI.
package auggieacp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `auggie --acp` from the exact binary resolved by the existing
// Auggie agent plugin. Models and slash commands come from the live ACP session
// advertisements. Auggie owns tools, rules, and auth; AO adds a session-scoped
// rules file and resolves permissions through ACP.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:          domain.HarnessAuggie,
		Configure:        configure,
		PermissionPolicy: permissionPolicy,
		SessionOptions:   sessionOptions,
	}, log)
}

// configure builds the ACP launch. Auggie has no CLI flag that injects standing
// instructions as text, so AO writes the session's system prompt to a
// session-private rules file and passes it through Auggie's repeatable
// `--rules` flag.
func configure(ctx context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		path, err := writeStandingRules(ctx, cfg, prompt)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--rules", path)
	}
	return args, nil, nil
}

func writeStandingRules(ctx context.Context, cfg acpdriver.LaunchConfig, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return "", fmt.Errorf("auggie ACP standing instructions require AO data directory")
	}
	dir := filepath.Join(cfg.DataDir, "prompts", string(cfg.SessionID), "auggie-acp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Auggie ACP rules directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "ao-standing-rules.md")
	if err := os.WriteFile(path, []byte(prompt+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write Auggie ACP standing rules: %w", err)
	}
	return path, nil
}

// permissionPolicy resolves the provider's exact offered choices for AO's
// stronger modes before the generic client parks a request for a human. Auggie
// offers no blanket auto-approve flag, so accept-edits and bypass are resolved
// per request; default and auto keep the ordinary approval flow.
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
	case ports.PermissionModeBypassPermissions:
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

// sessionOptions maps AO's durable model choice onto Auggie's advertised ACP
// model selector. The generic transport routes it through session/set_model.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}
