package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeResumeSessionStartConfirmsCurrentRuntimeLaunch(t *testing.T) {
	// Keep the installed Claude resume hook and lifecycle's interpretation of
	// that hook in one regression boundary. Either side can remain internally
	// valid while a drift between them strands a resumed native conversation on
	// its previous runtime launch.
	workspace := t.TempDir()
	if err := (&claudecode.Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("install Claude hooks: %v", err)
	}

	settingsPath := filepath.Join(workspace, ".claude", "settings.local.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	var settings struct {
		Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("decode Claude settings: %v", err)
	}

	const command = "ao hooks claude-code session-start"
	var matcher string
	for _, group := range settings.Hooks["SessionStart"] {
		for _, hook := range group.Hooks {
			if hook.Command == command && group.Matcher != nil {
				matcher = *group.Matcher
			}
		}
	}
	sources := strings.FieldsFunc(matcher, func(r rune) bool { return r == '|' || r == ',' })
	for index := range sources {
		sources[index] = strings.TrimSpace(sources[index])
	}
	for _, source := range []string{"startup", "resume", "clear", "compact", "fork"} {
		if !slices.Contains(sources, source) {
			t.Fatalf("SessionStart matcher %q does not include %q", matcher, source)
		}
	}

	manager, store, _ := newManager()
	session := working("claude-resume")
	session.Metadata.RuntimeLaunchID = "launch-resumed"
	session.Metadata.AgentSessionID = "claude-native"
	session.Metadata.AgentSessionIDLaunchID = "launch-before-resume"
	store.sessions[session.ID] = session

	if err := manager.ApplyActivitySignal(context.Background(), session.ID, ports.ActivitySignal{
		Event:          "session-start",
		LaunchID:       "launch-resumed",
		AgentSessionID: "claude-native",
	}); err != nil {
		t.Fatalf("apply resume SessionStart: %v", err)
	}

	got := store.sessions[session.ID].Metadata
	if got.AgentSessionID != "claude-native" || got.AgentSessionIDLaunchID != "launch-resumed" {
		t.Fatalf("native identity = id:%q launch:%q, want id:%q launch:%q",
			got.AgentSessionID, got.AgentSessionIDLaunchID, "claude-native", "launch-resumed")
	}
}
