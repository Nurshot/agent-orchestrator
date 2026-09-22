package session

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeIdentityResolver struct {
	identity ports.SCMIdentity
	err      error
}

func (f *fakeIdentityResolver) AuthenticatedIdentityForProvider(context.Context, string, string) (ports.SCMIdentity, error) {
	return f.identity, f.err
}

func TestGithubActorGatesOnIdentity(t *testing.T) {
	human := ports.SCMIdentity{Login: "octocat", Human: true}
	cases := []struct {
		name      string
		identity  ports.ScopedIdentityResolver
		wantLogin string
		wantOK    bool
	}{
		{
			name:      "human account resolves",
			identity:  &fakeIdentityResolver{identity: human},
			wantLogin: "octocat",
			wantOK:    true,
		},
		{
			name:     "resolver nil stays anonymous",
			identity: nil,
		},
		{
			name:     "identity error stays anonymous",
			identity: &fakeIdentityResolver{err: errors.New("GET /user failed")},
		},
		{
			name:     "non-human account stays anonymous",
			identity: &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "acme-org", Human: false}},
		},
		{
			name:     "empty login stays anonymous",
			identity: &fakeIdentityResolver{identity: ports.SCMIdentity{Login: "", Human: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{githubIdentity: tc.identity}
			login, ok := svc.githubActor(context.Background())
			if ok != tc.wantOK || login != tc.wantLogin {
				t.Fatalf("githubActor = (%q, %v), want (%q, %v)", login, ok, tc.wantLogin, tc.wantOK)
			}
		})
	}
}

// bestEffortResolver implements both the authoritative and the best-effort
// identity interfaces so githubActor's fallback tier can be exercised.
type bestEffortResolver struct {
	identity      ports.SCMIdentity
	identityErr   error
	bestEffort    string
	bestEffortErr error
	beCalls       int
}

func (f *bestEffortResolver) AuthenticatedIdentityForProvider(context.Context, string, string) (ports.SCMIdentity, error) {
	return f.identity, f.identityErr
}

func (f *bestEffortResolver) BestEffortLoginForProvider(context.Context, string, string) (string, error) {
	f.beCalls++
	return f.bestEffort, f.bestEffortErr
}

func TestGithubActorFallsBackToBestEffort(t *testing.T) {
	t.Run("authoritative human wins, best-effort not consulted", func(t *testing.T) {
		r := &bestEffortResolver{identity: ports.SCMIdentity{Login: "octocat", Human: true}, bestEffort: "ssh-user"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if !ok || login != "octocat" {
			t.Fatalf("githubActor = (%q, %v), want (octocat, true)", login, ok)
		}
		if r.beCalls != 0 {
			t.Fatalf("best-effort called %d times; want 0 when authoritative succeeds", r.beCalls)
		}
	})

	t.Run("best-effort used when authoritative fails", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: errors.New("no token"), bestEffort: "Pulkit7070"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if !ok || login != "Pulkit7070" {
			t.Fatalf("githubActor = (%q, %v), want (Pulkit7070, true)", login, ok)
		}
	})

	t.Run("best-effort used when authoritative is non-human", func(t *testing.T) {
		r := &bestEffortResolver{identity: ports.SCMIdentity{Login: "acme-org", Human: false}, bestEffort: "Pulkit7070"}
		login, ok := (&Service{githubIdentity: r}).githubActor(context.Background())
		if !ok || login != "Pulkit7070" {
			t.Fatalf("githubActor = (%q, %v), want (Pulkit7070, true)", login, ok)
		}
	})

	t.Run("anonymous when both tiers empty", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: errors.New("no token"), bestEffort: ""}
		if login, ok := (&Service{githubIdentity: r}).githubActor(context.Background()); ok || login != "" {
			t.Fatalf("githubActor = (%q, %v), want empty anonymous", login, ok)
		}
	})

	t.Run("anonymous when best-effort errors", func(t *testing.T) {
		r := &bestEffortResolver{identityErr: errors.New("no token"), bestEffortErr: errors.New("ssh failed")}
		if login, ok := (&Service{githubIdentity: r}).githubActor(context.Background()); ok || login != "" {
			t.Fatalf("githubActor = (%q, %v), want empty anonymous", login, ok)
		}
	})
}
