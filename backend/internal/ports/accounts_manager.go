package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// AccountsManagerLaunchRoute is private launch material. It must only be
// carried to the child process being launched and must never enter public
// session DTOs, logs, or durable session metadata.
type AccountsManagerLaunchRoute struct {
	BaseURL string
	Token   string
}

type AccountsManagerLaunchRouter interface {
	PrepareAgentLaunchRoute(context.Context, domain.SessionID, domain.AccountsManagerProvider, string) (*AccountsManagerLaunchRoute, error)
	AgentRoutingEnabled(context.Context, domain.AccountsManagerProvider) (bool, error)
}
