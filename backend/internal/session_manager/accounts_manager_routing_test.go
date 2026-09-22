package sessionmanager

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeAccountsManagerRouter struct {
	route   *ports.AccountsManagerLaunchRoute
	err     error
	enabled bool
}

func (f fakeAccountsManagerRouter) PrepareAgentLaunchRoute(context.Context, domain.SessionID, domain.AccountsManagerProvider, string) (*ports.AccountsManagerLaunchRoute, error) {
	return f.route, f.err
}

func (f fakeAccountsManagerRouter) AgentRoutingEnabled(context.Context, domain.AccountsManagerProvider) (bool, error) {
	return f.enabled, f.err
}

func TestPrepareAccountsManagerRouteInjectsOnlyChildScopedCodexConfiguration(t *testing.T) {
	m := New(Deps{AccountsManager: fakeAccountsManagerRouter{route: &ports.AccountsManagerLaunchRoute{BaseURL: "http://127.0.0.1:43127", Token: "opaque-token"}}})
	env := map[string]string{"OPENAI_API_KEY": "native-key"}
	route, err := m.prepareAccountsManagerRoute(context.Background(), "session-1", domain.HarnessCodex, "gpt-5", env)
	if err != nil {
		t.Fatal(err)
	}
	if route == nil || route.BaseURL != "http://127.0.0.1:43127" || route.TokenEnv != accountsManagerCodexTokenEnv {
		t.Fatalf("route = %#v", route)
	}
	if env[accountsManagerCodexTokenEnv] != "opaque-token" || env["OPENAI_API_KEY"] != "native-key" {
		t.Fatalf("env = %#v", env)
	}
}

func TestPrepareAccountsManagerRouteReplacesConflictingClaudeCredentials(t *testing.T) {
	m := New(Deps{AccountsManager: fakeAccountsManagerRouter{route: &ports.AccountsManagerLaunchRoute{BaseURL: "http://127.0.0.1:43127/", Token: "opaque-token"}}})
	env := map[string]string{"anthropic_api_key": "native-key", "Claude_Code_OAuth_Token": "native-oauth"}
	_, err := m.prepareAccountsManagerRoute(context.Background(), "session-1", domain.HarnessClaudeCode, "sonnet", env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["anthropic_api_key"]; ok {
		t.Fatal("case-insensitive ANTHROPIC_API_KEY was not removed")
	}
	if _, ok := env["Claude_Code_OAuth_Token"]; ok {
		t.Fatal("case-insensitive CLAUDE_CODE_OAUTH_TOKEN was not removed")
	}
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:43127" || env["ANTHROPIC_AUTH_TOKEN"] != "opaque-token" {
		t.Fatalf("env = %#v", env)
	}
}

func TestPrepareAccountsManagerRouteLeavesNativeEnvironmentUntouchedWhenOff(t *testing.T) {
	m := New(Deps{AccountsManager: fakeAccountsManagerRouter{}})
	env := map[string]string{"ANTHROPIC_API_KEY": "native-key"}
	route, err := m.prepareAccountsManagerRoute(context.Background(), "session-1", domain.HarnessClaudeCode, "sonnet", env)
	if err != nil || route != nil || env["ANTHROPIC_API_KEY"] != "native-key" {
		t.Fatalf("route=%#v env=%#v err=%v", route, env, err)
	}
}
