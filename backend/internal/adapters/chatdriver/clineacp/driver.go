// Package clineacp binds the user's own Cline CLI installation to AO's reusable
// ACP Chat transport.
//
// Cline exposes ACP natively via `cline --acp`; AO launches the exact binary
// resolved by the existing Cline agent plugin, so sign-in, models, providers,
// organization billing, and settings remain the user's own.
package clineacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `cline --acp` from the exact binary resolved by the existing
// Cline agent plugin. Model and provider catalogs, Plan/Act modes, and the
// per-session auto-approve toggle come from the live ACP session
// advertisements. Cline owns tools and credentials; AO resolves permissions
// through ACP.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessCline,
		Configure:            configure,
		PermissionPolicy:     permissionPolicy,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

// configure builds the `cline --acp` argv. Cline's ACP entrypoint reads its
// launch defaults from CLINE_MODEL/CLINE_PROVIDER rather than the `--model`
// flag, which is ignored once `--acp` is set, so the model is delivered as an
// environment override. Only an explicit auto/bypass choice enables
// `--auto-approve true`; Cline deliberately refuses to auto-approve ACP
// sessions otherwise.
//
// Standing instructions are not injectable in ACP mode: Cline ignores `--system`
// there and reads its rules from the workspace, so AO's role prompt is not
// forwarded. This is documented in docs/harnesses/acp-bindings.md.
func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	switch ports.NormalizePermissionMode(cfg.Permissions) {
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		args = append(args, "--auto-approve", "true")
	}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		return args, map[string]string{"CLINE_MODEL": model}, nil
	}
	return args, nil, nil
}

// permissionPolicy resolves the provider's exact offered choices for AO's
// accept-edits mode before the generic client parks a request for a human.
// Cline exposes no edit-only auto-approve flag, so the mapping is per request;
// default and auto keep the ordinary approval flow.
func permissionPolicy(
	mode ports.PermissionMode,
	params acpsdk.RequestPermissionRequest,
) (acpsdk.PermissionOptionId, bool) {
	if ports.NormalizePermissionMode(mode) != ports.PermissionModeAcceptEdits {
		return "", false
	}
	kind := acpsdk.ToolKind("")
	if params.ToolCall.Kind != nil {
		kind = *params.ToolCall.Kind
	}
	if kind != acpsdk.ToolKindEdit && kind != acpsdk.ToolKindDelete && kind != acpsdk.ToolKindMove {
		return "", false
	}
	return permissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
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

// sessionOptions maps AO's durable model choice onto Cline's advertised "model"
// config option. The generic transport routes it through
// session/set_config_option.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}

// validateTurnSettings rejects approval changes a Cline ACP session cannot make.
// Auto-approval is fixed by the launch flags and Cline's boolean
// session/set_config_option encoding is not applied by AO's string-valued
// option surface, so a change requires restarting Chat.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	if ports.NormalizePermissionMode(settings.Approval) == ports.NormalizePermissionMode(initial) {
		return nil
	}
	return fmt.Errorf(
		"%w: Cline ACP auto-approval is fixed at process launch (%s); restart Chat to run it in %s",
		acpdriver.ErrACPSetterUnsupported,
		ports.NormalizePermissionMode(initial), ports.NormalizePermissionMode(settings.Approval))
}
