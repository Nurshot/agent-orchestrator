// Package primeagentacp binds the user's own Prime Agent installation to AO's
// reusable ACP Chat transport.
//
// Prime Agent exposes ACP natively via `prime-agent --mode acp`; AO launches the
// exact binary resolved by the existing Prime Agent plugin, so providers,
// models, skills, and credentials remain the user's own.
package primeagentacp

import (
	"context"
	"log/slog"
	"strings"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `prime-agent --mode acp` from the exact binary resolved by the
// existing Prime Agent plugin.
//
// Prime Agent's ACP profile deliberately exposes one session per connection and
// no session/load or session/resume, so AO does not claim native history
// recovery. Its ACP mode is a trusted-code boundary with no permission
// requests, so approvals are reported unsupported and AO admits Prime Agent
// Chat only through the explicit per-session bypass fallback, matching Pi.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessPrimeAgent,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true,
			ports.ChatCapabilityTools:     true,
			ports.ChatCapabilityApprovals: false,
			ports.ChatCapabilityInterrupt: true,
			ports.ChatCapabilityResume:    false,
		},
		Configure:      configure,
		SessionOptions: sessionOptions,
	}, log)
}

// configure builds the `prime-agent --mode acp` argv. Prime Agent reads standing
// instructions from its own AGENTS.md/config surfaces and accepts no system
// prompt or model flag in ACP mode, so configure forwards neither.
func configure(_ context.Context, _ acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return []string{"--mode", "acp"}, nil, nil
}

// sessionOptions maps AO's durable model and effort choices onto Prime Agent's
// advertised config option ids ("model" and "thought_level").
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if model := strings.TrimSpace(settings.Model); model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: model})
	}
	if effort := strings.TrimSpace(settings.Effort); effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "thought_level", Value: effort})
	}
	return options
}
