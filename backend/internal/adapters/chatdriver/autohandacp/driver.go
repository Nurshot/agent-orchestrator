// Package autohandacp binds the user's own Autohand CLI installation to AO's
// reusable ACP Chat transport through Autohand's independently installed
// `autohand-acp` adapter.
//
// Autohand ships its ACP server as a separate npm distribution
// (`@autohandai/autohand-acp`) rather than a flag on the CLI. AO launches that
// exact user-installed binary and keeps the existing Autohand agent plugin as
// the canonical auth probe; login, models, modes, skills, and MCP
// configuration remain the user's own. AO never downloads the adapter.
package autohandacp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// permissionModeEnvVar selects Autohand's permission handling. "external"
// routes tool approvals to the ACP client (AO) instead of Autohand deciding
// them; that is what keeps AO's approval vocabulary authoritative for Chat.
const permissionModeEnvVar = "AUTOHAND_PERMISSION_MODE"

// autohandPlugin is the subset of AO's existing Autohand agent plugin the Chat
// driver reuses for binary resolution and local auth probing.
type autohandPlugin interface {
	ResolveBinary(context.Context) (string, error)
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

// New constructs Autohand's Chat driver over the existing Autohand agent plugin.
func New(plugin autohandPlugin, log *slog.Logger) ports.ChatDriver {
	return acpdriver.New(acpdriver.Config{
		Harness: domain.HarnessAutohand,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityTools:     true,
			ports.ChatCapabilityApprovals: true,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    true,
		},
		Probe: func(ctx context.Context) error {
			if _, err := resolveAdapterBinary(ctx, plugin); err != nil {
				return fmt.Errorf("%w: autohand-acp is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			status, err := plugin.AuthStatus(ctx)
			if err == nil && status == ports.AgentAuthStatusUnauthorized {
				return ports.ErrChatAuthRequired
			}
			if err != nil && log != nil {
				log.Debug("Autohand auth probe inconclusive; continuing", "error", err)
			}
			return nil
		},
		Launch: func(ctx context.Context, cfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			binary, err := resolveAdapterBinary(ctx, plugin)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: autohand-acp is not installed: %w", ports.ErrChatDriverUnavailable, err)
			}
			env := make(map[string]string, len(cfg.Env)+1)
			for key, value := range cfg.Env {
				env[key] = value
			}
			env[permissionModeEnvVar] = "external"
			return acpdriver.Launch{Command: binary, Env: env}, nil
		},
		PermissionPolicy: permissionPolicy,
	}, log)
}

// resolveAdapterBinary finds the `autohand-acp` executable. npm global installs
// place it beside the `autohand` binary, so the sibling of the plugin-resolved
// binary is preferred; PATH and common user install locations are the fallback.
func resolveAdapterBinary(ctx context.Context, plugin autohandPlugin) (string, error) {
	autohandBinary, err := plugin.ResolveBinary(ctx)
	if err != nil {
		return "", err
	}
	if sibling := siblingAdapter(autohandBinary); sibling != "" {
		return sibling, nil
	}
	return binaryutil.ResolveBinary(ctx, autohandACPSpec)
}

func siblingAdapter(autohandBinary string) string {
	dir := filepath.Dir(autohandBinary)
	candidates := []string{"autohand-acp"}
	if runtime.GOOS == "windows" {
		candidates = []string{"autohand-acp.cmd", "autohand-acp.exe", "autohand-acp"}
	}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

var autohandACPSpec = binaryutil.BinarySpec{
	Label:         "autohand-acp",
	Names:         []string{"autohand-acp"},
	WinNames:      []string{"autohand-acp.cmd", "autohand-acp.exe", "autohand-acp"},
	UnixPaths:     []string{"/usr/local/bin/autohand-acp", "/opt/homebrew/bin/autohand-acp"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("autohand-acp"),
	NodeManaged:   true,
}

// permissionPolicy resolves Autohand's exact offered choices for AO's stronger
// modes before the generic client parks a request for a human. accept-edits
// allows file-changing tools once; auto and bypass allow every tool. Default
// keeps the ordinary approval flow.
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
