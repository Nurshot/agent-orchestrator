//go:build darwin || linux

package unrealagent

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type authPlugin struct{ status ports.AgentAuthStatus }

func (plugin authPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return plugin.status, nil
}

func TestDriverRequiresExplicitBypassPermissions(t *testing.T) {
	driver := New(authPlugin{status: ports.AgentAuthStatusAuthorized}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	caps, err := driver.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if missing := ports.MissingCapabilitiesForPermissions(caps, ports.PermissionModeBypassPermissions); len(missing) != 0 {
		t.Fatalf("bypass production capabilities missing %v", missing)
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeBypassPermissions, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeDefault, false, nil, nil); err == nil {
		t.Fatal("default permissions unexpectedly accepted without an approval channel")
	}
}

func TestDriverProbeRequiresProviderAuthentication(t *testing.T) {
	driver := New(authPlugin{status: ports.AgentAuthStatusUnauthorized}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	if _, err := driver.Probe(t.Context()); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("Probe() error = %v, want ErrChatAuthRequired", err)
	}
}
