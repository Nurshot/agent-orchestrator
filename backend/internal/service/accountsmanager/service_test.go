package accountsmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/accountsmanager"
)

type fakeClient struct {
	credentials []core.CredentialSummary
	listErr     error
	stream      chan core.OAuthEvent
}

func (f *fakeClient) ListCredentials(context.Context) ([]core.CredentialSummary, error) {
	return f.credentials, f.listErr
}
func (f *fakeClient) CredentialPublicID(ref string) (string, error) { return "safe-" + ref, nil }
func (f *fakeClient) OAuthPublicID(state string) (string, error)    { return "oauth-" + state, nil }
func (f *fakeClient) StreamOAuthEvents(ctx context.Context, consume func(core.OAuthEvent) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-f.stream:
			if err := consume(event); err != nil {
				return err
			}
		}
	}
}

func TestServicePublishesSafeSnapshotsAndPreservesStaleAccounts(t *testing.T) {
	fake := &fakeClient{credentials: []core.CredentialSummary{{Ref: "raw-ref", Provider: core.ProviderCodex, Kind: core.CredentialOAuth, Email: "a@example.com", Status: core.CredentialActive}}, stream: make(chan core.OAuthEvent, 1)}
	svc := New(fake)
	got, err := svc.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "safe-raw-ref" || got.Accounts[0].ID == "raw-ref" || got.Stale {
		t.Fatalf("snapshot = %#v", got)
	}
	fake.listErr = errors.New("runner down")
	stale, err := svc.Refresh(context.Background())
	if err == nil || !stale.Stale || len(stale.Accounts) != 1 {
		t.Fatalf("stale snapshot = %#v, %v", stale, err)
	}
}

func TestServiceMapsOAuthEventsWithoutExposingState(t *testing.T) {
	fake := &fakeClient{stream: make(chan core.OAuthEvent, 1)}
	svc := New(fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	fake.stream <- core.OAuthEvent{Provider: core.ProviderClaude, State: "raw-state", Status: core.OAuthCompleted, ExpiresAt: time.Now().Add(time.Minute)}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := svc.Snapshot()
		if len(snapshot.OAuthSessions) == 1 {
			if snapshot.OAuthSessions[0].ID != "oauth-raw-state" || snapshot.OAuthSessions[0].ID == "raw-state" {
				t.Fatalf("session = %#v", snapshot.OAuthSessions[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("OAuth event was not published")
}
