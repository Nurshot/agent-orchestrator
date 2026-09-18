package opencodeacp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesNativeACPAndMergesSystemPromptConfig(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-1", SystemPrompt: "Follow AO worker rules.",
		Permissions: ports.PermissionModeBypassPermissions,
		Env: map[string]string{
			"OPENCODE_CONFIG":         "/user/custom.json",
			"OPENCODE_CONFIG_CONTENT": `{"provider":{"local":{"name":"Local"}},"agent":{"mine":{"mode":"primary"}}}`,
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(args) != 1 || args[0] != "acp" {
		t.Fatalf("args = %#v", args)
	}
	var config struct {
		DefaultAgent string         `json:"default_agent"`
		Provider     map[string]any `json:"provider"`
		Permission   string         `json:"permission"`
		Agent        map[string]struct {
			Mode   string `json:"mode"`
			Prompt string `json:"prompt"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config.DefaultAgent != "ao-worker-1" {
		t.Fatalf("default agent = %q", config.DefaultAgent)
	}
	if got := config.Agent[config.DefaultAgent]; got.Mode != "primary" || got.Prompt != "Follow AO worker rules." {
		t.Fatalf("AO agent config = %#v", got)
	}
	if _, ok := config.Agent["mine"]; !ok || config.Provider["local"] == nil {
		t.Fatalf("user inline config was not preserved: %#v", config)
	}
	if config.Permission != "" {
		t.Fatalf("permission = %q; launch-time rules cannot be taken back mid-session", config.Permission)
	}
}

func TestConfigureWithoutSystemPromptWritesNoAOConfig(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(args) != 1 || args[0] != "acp" || env != nil {
		t.Fatalf("args/env = %#v, %#v", args, env)
	}
}

func TestSessionOptionsUseProviderAdvertisedModelOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "anthropic/claude-sonnet"})
	if len(got) != 1 || got[0].ID != "model" || got[0].Value != "anthropic/claude-sonnet" {
		t.Fatalf("model settings = %#v", got)
	}
}

func TestRejectsProviderNameBeforeLaunchingOpenCode(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "start"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			plugin := &unresolvedPlugin{}
			driver := New(plugin, nil)
			cfg := ports.ChatStartConfig{WorkspacePath: t.TempDir(), Model: "TensorMux"}
			var err error
			if resume {
				_, err = driver.Resume(context.Background(), ports.ChatResumeConfig{WorkspacePath: cfg.WorkspacePath, Model: cfg.Model, ProviderConversationID: "existing"})
			} else {
				_, err = driver.Start(context.Background(), cfg)
			}
			if !errors.Is(err, ports.ErrChatConfigOptionInvalid) || !strings.Contains(err.Error(), "provider/model") {
				t.Fatalf("error = %v, want model format validation with recovery guidance", err)
			}
			if plugin.resolved {
				t.Fatal("invalid model reached OpenCode binary resolution")
			}
		})
	}
}

type unresolvedPlugin struct{ resolved bool }

func (p *unresolvedPlugin) ResolveBinary(context.Context) (string, error) {
	p.resolved = true
	return "", errors.New("unexpected binary resolution")
}
func (*unresolvedPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}

func TestValidateTurnSettingsModelFormat(t *testing.T) {
	for _, model := range []string{"", "tensormux/glm-4-7-flash", "openrouter/vendor/model", "custom-provider/private-model:latest"} {
		t.Run("valid/"+model, func(t *testing.T) {
			settings := ports.ChatTurnSettings{Model: model}
			if err := validateTurnSettings(ports.PermissionModeDefault, settings); err != nil {
				t.Fatal(err)
			}
			options := sessionOptions(settings)
			if model != "" && (len(options) != 1 || options[0].Value != model) {
				t.Fatalf("model ID changed: %#v", options)
			}
		})
	}
	for _, model := range []string{"TensorMux", "glm-4-7-flash", " ", "/model", "provider/", " /model", "provider/ "} {
		t.Run("invalid/"+model, func(t *testing.T) {
			err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{Model: model})
			if !errors.Is(err, ports.ErrChatConfigOptionInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSessionOptionsForwardsEffortAfterModel(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{Model: "opencode-go/deepseek-v4.1-flash", Effort: "max"})
	if len(got) != 2 || got[0].ID != "model" || got[1].ID != "effort" || got[1].Value != "max" {
		t.Fatalf("model+effort settings = %#v", got)
	}
	got = sessionOptions(ports.ChatTurnSettings{Effort: "xhigh"})
	if len(got) != 1 || got[0].ID != "effort" || got[0].Value != "xhigh" {
		t.Fatalf("effort-only settings = %#v", got)
	}
}

func TestPermissionPolicyTiersFollowToolKind(t *testing.T) {
	options := []acpsdk.PermissionOption{
		{OptionId: "reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
		{OptionId: "once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "always", Kind: acpsdk.PermissionOptionKindAllowAlways},
	}
	// OpenCode reports bash as execute, webfetch as fetch, and anything it does
	// not classify (MCP tools, skills) as other.
	edit, execute, fetch, other := acpsdk.ToolKindEdit, acpsdk.ToolKindExecute, acpsdk.ToolKindFetch, acpsdk.ToolKindOther
	tests := []struct {
		name   string
		mode   ports.PermissionMode
		kind   *acpsdk.ToolKind
		wantID acpsdk.PermissionOptionId
	}{
		{name: "default asks about edits", mode: ports.PermissionModeDefault, kind: &edit},
		{name: "accept edits allows an edit", mode: ports.PermissionModeAcceptEdits, kind: &edit, wantID: "once"},
		{name: "accept edits asks about shell", mode: ports.PermissionModeAcceptEdits, kind: &execute},
		{name: "accept edits asks about fetch", mode: ports.PermissionModeAcceptEdits, kind: &fetch},
		{name: "auto allows an edit", mode: ports.PermissionModeAuto, kind: &edit, wantID: "once"},
		{name: "auto allows a fetch", mode: ports.PermissionModeAuto, kind: &fetch, wantID: "once"},
		{name: "auto asks about shell", mode: ports.PermissionModeAuto, kind: &execute},
		{name: "auto asks about unclassified tools", mode: ports.PermissionModeAuto, kind: &other},
		{name: "auto asks when the kind is missing", mode: ports.PermissionModeAuto},
		{name: "bypass allows shell persistently", mode: ports.PermissionModeBypassPermissions, kind: &execute, wantID: "always"},
		{name: "bypass allows an unknown kind", mode: ports.PermissionModeBypassPermissions, wantID: "always"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, handled := permissionPolicy(test.mode, acpsdk.RequestPermissionRequest{
				ToolCall: acpsdk.ToolCallUpdate{Kind: test.kind}, Options: options,
			})
			if id != test.wantID || handled != (test.wantID != "") {
				t.Fatalf("selection = (%q, %v), want (%q, %v)", id, handled, test.wantID, test.wantID != "")
			}
		})
	}

	if id, _ := permissionPolicy(ports.PermissionModeBypassPermissions,
		acpsdk.RequestPermissionRequest{Options: options[:2]}); id != "once" {
		t.Fatalf("bypass without allow-always = %q, want \"once\"", id)
	}
	if _, handled := permissionPolicy(ports.PermissionModeBypassPermissions,
		acpsdk.RequestPermissionRequest{Options: options[:1]}); handled {
		t.Fatal("bypass answered a request offering no allow option")
	}
}

func TestEveryPermissionModeSwitchesWhileTheSessionRuns(t *testing.T) {
	modes := []ports.PermissionMode{
		ports.PermissionModeDefault, ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto, ports.PermissionModeBypassPermissions,
	}
	for _, initial := range modes {
		for _, next := range modes {
			// Both directions: OpenCode is never launched with a permission rule,
			// so nothing has to be taken back and no restart is required.
			if err := validateTurnSettings(initial, ports.ChatTurnSettings{Approval: next}); err != nil {
				t.Fatalf("%q -> %q: %v", initial, next, err)
			}
		}
	}
}

func TestConfigureLeavesPermissionsToTheUsersOwnOpenCodeConfig(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		ports.PermissionModeDefault, ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto, ports.PermissionModeBypassPermissions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			_, env, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if env != nil {
				t.Fatalf("env = %#v; no system prompt and no permission rule means no overlay", env)
			}
		})
	}

	_, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-2", SystemPrompt: "Follow AO worker rules.",
		Permissions: ports.PermissionModeBypassPermissions,
		Env:         map[string]string{"OPENCODE_CONFIG_CONTENT": `{"permission":{"bash":"deny"}}`},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	var config struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config.Permission["bash"] != "deny" {
		t.Fatalf("permission = %#v, want the user's own rules untouched", config.Permission)
	}
}
