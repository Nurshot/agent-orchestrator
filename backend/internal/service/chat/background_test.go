package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type backgroundTestRegistry struct{ driver ports.ChatDriver }

func (r backgroundTestRegistry) Driver(domain.AgentHarness) (ports.ChatDriver, error) {
	if r.driver == nil {
		return nil, ports.ErrChatUnsupported
	}
	return r.driver, nil
}

func (r backgroundTestRegistry) SupportsChat(domain.AgentHarness) bool { return r.driver != nil }

type backgroundTestDriver struct {
	start ports.ChatStartConfig
	conv  *backgroundTestConversation
}

func (*backgroundTestDriver) Harness() domain.AgentHarness { return domain.HarnessCodex }
func (*backgroundTestDriver) Probe(context.Context) (ports.ChatCapabilities, error) {
	return nil, nil
}
func (d *backgroundTestDriver) Start(_ context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	d.start = cfg
	return d.conv, nil
}
func (*backgroundTestDriver) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	return nil, errors.New("not used")
}

type backgroundTestConversation struct {
	events     chan ports.ChatEvent
	message    ports.ChatUserMessage
	terminated bool
}

func newBackgroundTestConversation() *backgroundTestConversation {
	return &backgroundTestConversation{events: make(chan ports.ChatEvent, 2)}
}

func (*backgroundTestConversation) ProviderConversationID() string       { return "provider-1" }
func (*backgroundTestConversation) Capabilities() ports.ChatCapabilities { return nil }
func (c *backgroundTestConversation) SendTurn(_ context.Context, message ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	c.message = message
	return ports.ChatTurnRef{ProviderTurnID: "turn-1"}, nil
}
func (c *backgroundTestConversation) StartDeferredTurn(string) error {
	c.events <- ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "turn-1", Text: "Fix renderer"}
	c.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "turn-1", TurnState: domain.TurnStateCompleted}
	return nil
}
func (*backgroundTestConversation) DiscardDeferredTurn(string)              {}
func (*backgroundTestConversation) Interrupt(context.Context, string) error { return nil }
func (*backgroundTestConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return nil
}
func (c *backgroundTestConversation) Events() <-chan ports.ChatEvent { return c.events }
func (*backgroundTestConversation) Close() error                     { return nil }
func (c *backgroundTestConversation) Terminate() error {
	c.terminated = true
	return nil
}

func TestRunBackgroundTaskUsesNativeDriverAndTerminatesIt(t *testing.T) {
	conversation := newBackgroundTestConversation()
	driver := &backgroundTestDriver{conv: conversation}
	ids := []string{"task-id", "scope-id"}
	service := New(Options{
		Drivers: backgroundTestRegistry{driver: driver},
		NewID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})

	title, err := service.RunBackgroundTask(context.Background(), domain.HarnessCodex, ports.ChatStartConfig{
		DataDir: "/data", WorkspacePath: "/workspace",
		Env: map[string]string{"CODEX_HOME": "/account"}, Model: "small", Effort: "low",
		Permissions: ports.PermissionModeAuto, SystemPrompt: "title only",
	}, "Fix the renderer")
	if err != nil || title != "Fix renderer" {
		t.Fatalf("RunBackgroundTask = %q, %v", title, err)
	}
	if driver.start.SessionID != "background-task-id" || driver.start.ProviderScopeID != "scope-id" || !driver.start.ProviderIDsScoped {
		t.Fatalf("start identity = %#v", driver.start)
	}
	if driver.start.Model != "small" || driver.start.Effort != "low" || driver.start.Env["CODEX_HOME"] != "/account" {
		t.Fatalf("start config = %#v", driver.start)
	}
	if conversation.message.Text != "Fix the renderer" || conversation.message.Origin != domain.MessageOriginAutomation {
		t.Fatalf("message = %#v", conversation.message)
	}
	if !conversation.terminated {
		t.Fatal("background provider was not terminated")
	}
}
