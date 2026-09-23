package unrealagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus reports whether the selected provider has credentials available.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	provider := strings.TrimSpace(os.Getenv("UNREAL_HARNESS_LLM_PROVIDER"))
	if provider == "" {
		provider = "openai"
	}
	genericKey := strings.TrimSpace(os.Getenv("UNREAL_HARNESS_LLM_API_KEY")) != ""
	switch provider {
	case "ollama":
		return ports.AgentAuthStatusAuthorized, nil
	case "openai":
		return credentialStatus(genericKey || envSet("OPENAI_API_KEY")), nil
	case "openrouter":
		return credentialStatus(genericKey || envSet("OPENROUTER_API_KEY")), nil
	case "fireworks":
		return credentialStatus(genericKey || envSet("FIREWORKS_API_KEY")), nil
	case "openai-codex":
		return credentialStatus(envSet("OPENAI_CODEX_ACCESS_TOKEN") || codexAuthFilePresent()), nil
	default:
		return ports.AgentAuthStatusUnknown, nil
	}
}

func credentialStatus(present bool) ports.AgentAuthStatus {
	if present {
		return ports.AgentAuthStatusAuthorized
	}
	return ports.AgentAuthStatusUnauthorized
}

func envSet(name string) bool { return strings.TrimSpace(os.Getenv(name)) != "" }

func codexAuthFilePresent() bool {
	path := strings.TrimSpace(os.Getenv("OPENAI_CODEX_AUTH_FILE"))
	if path == "" {
		home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil || home == "" {
				return false
			}
			home = filepath.Join(home, ".codex")
		}
		path = filepath.Join(home, "auth.json")
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}
