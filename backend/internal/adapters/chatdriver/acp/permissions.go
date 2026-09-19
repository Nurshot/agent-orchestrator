package acp

import (
	"fmt"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// PermissionOption returns the id of the first choice the provider offered with
// this kind. Bindings resolve AO's permission modes against the exact options in
// the request rather than guessing an id, because the vocabulary is the
// provider's.
func PermissionOption(
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

// StandardPermissionPolicy builds the policy shared by every binding whose
// provider offers ACP's ordinary allow-once/allow-always choices: accept-edits
// allows a file-changing tool once, each mode in blanketAllow allows any tool
// (persistently where the provider offers it), and anything else keeps the
// ordinary approval flow by parking the request for a human.
//
// Bindings differ only in which modes they can blanket-allow over ACP: a
// provider with a launch-time auto-approve flag passes fewer, because that flag
// already settled those modes before the session opened.
func StandardPermissionPolicy(blanketAllow ...ports.PermissionMode) PermissionPolicy {
	allowAny := make(map[ports.PermissionMode]bool, len(blanketAllow))
	for _, mode := range blanketAllow {
		allowAny[ports.NormalizePermissionMode(mode)] = true
	}
	return func(
		mode ports.PermissionMode,
		params acpsdk.RequestPermissionRequest,
	) (acpsdk.PermissionOptionId, bool) {
		switch mode = ports.NormalizePermissionMode(mode); {
		case mode == ports.PermissionModeAcceptEdits:
			if !changesFiles(params.ToolCall.Kind) {
				return "", false
			}
			return PermissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
		case allowAny[mode]:
			if id, ok := PermissionOption(params.Options, acpsdk.PermissionOptionKindAllowAlways); ok {
				return id, true
			}
			return PermissionOption(params.Options, acpsdk.PermissionOptionKindAllowOnce)
		default:
			return "", false
		}
	}
}

// changesFiles reports whether a tool call writes to the workspace. An absent
// kind is not assumed to be an edit.
func changesFiles(kind *acpsdk.ToolKind) bool {
	if kind == nil {
		return false
	}
	switch *kind {
	case acpsdk.ToolKindEdit, acpsdk.ToolKindDelete, acpsdk.ToolKindMove:
		return true
	default:
		return false
	}
}

// ApprovalFixedAtLaunch builds the turn-settings validator for a binding whose
// effective approval mode is decided by the launch argv or environment. A live
// change cannot take effect, so it is rejected with ErrACPSetterUnsupported and
// the user is told to restart Chat, rather than silently ignored.
//
// subject names the provider surface in the error, for example
// "Cline ACP auto-approval".
func ApprovalFixedAtLaunch(subject string) TurnSettingsValidator {
	return func(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
		if settings.Approval == "" {
			return nil
		}
		launched := ports.NormalizePermissionMode(initial)
		requested := ports.NormalizePermissionMode(settings.Approval)
		if launched == requested {
			return nil
		}
		return fmt.Errorf(
			"%w: %s is fixed at process launch (%s); restart Chat to run it in %s",
			ErrACPSetterUnsupported, subject, launched, requested)
	}
}
