package accountsmanager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/accountsmanager"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeClient struct {
	credentials []core.CredentialSummary
	listErr     error
	stream      chan core.OAuthEvent
	minted      []string
	mintErr     map[string]error
}

func (f *fakeClient) MintRoute(_ context.Context, provider core.Provider, ref, sessionID string) (core.RouteCapability, error) {
	if err := f.mintErr[ref]; err != nil {
		return core.RouteCapability{}, err
	}
	f.minted = append(f.minted, ref)
	return core.RouteCapability{BaseURL: "http://127.0.0.1:43127", Token: "opaque-" + ref}, nil
}

type fakeRoutingStore struct {
	mu       sync.Mutex
	policies map[domain.AccountsManagerProvider]domain.AccountsManagerRoutingPolicy
	routes   map[string]domain.AccountsManagerSessionRoute
}

func newFakeRoutingStore() *fakeRoutingStore {
	return &fakeRoutingStore{policies: make(map[domain.AccountsManagerProvider]domain.AccountsManagerRoutingPolicy), routes: make(map[string]domain.AccountsManagerSessionRoute)}
}

func (f *fakeRoutingStore) GetAccountsManagerRoutingPolicy(_ context.Context, provider domain.AccountsManagerProvider) (domain.AccountsManagerRoutingPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	policy, ok := f.policies[provider]
	if !ok {
		return domain.AccountsManagerRoutingPolicy{Provider: provider, AccountIDs: []string{}}, nil
	}
	policy.AccountIDs = append([]string(nil), policy.AccountIDs...)
	return policy, nil
}
func (f *fakeRoutingStore) PutAccountsManagerRoutingPolicy(_ context.Context, policy domain.AccountsManagerRoutingPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	policy.AccountIDs = append([]string(nil), policy.AccountIDs...)
	f.policies[policy.Provider] = policy
	return nil
}
func (f *fakeRoutingStore) GetAccountsManagerSessionRoute(_ context.Context, sessionID domain.SessionID, provider domain.AccountsManagerProvider) (domain.AccountsManagerSessionRoute, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	route, ok := f.routes[string(sessionID)+":"+string(provider)]
	return route, ok, nil
}
func (f *fakeRoutingStore) GetOrCreateAccountsManagerSessionRoute(_ context.Context, route domain.AccountsManagerSessionRoute) (domain.AccountsManagerSessionRoute, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(route.SessionID) + ":" + string(route.Provider)
	if existing, ok := f.routes[key]; ok {
		return existing, false, nil
	}
	f.routes[key] = route
	return route, true, nil
}

func TestPrepareLaunchRoutePinsFirstUsableAccountAndNeverRepins(t *testing.T) {
	client := &fakeClient{credentials: []core.CredentialSummary{
		{Ref: "primary", Provider: core.ProviderCodex, Status: core.CredentialActive, Unavailable: true},
		{Ref: "fallback", Provider: core.ProviderCodex, Status: core.CredentialActive},
		{Ref: "other", Provider: core.ProviderCodex, Status: core.CredentialActive},
	}, stream: make(chan core.OAuthEvent), mintErr: make(map[string]error)}
	store := newFakeRoutingStore()
	store.policies[domain.AccountsManagerProviderCodex] = domain.AccountsManagerRoutingPolicy{
		Provider: domain.AccountsManagerProviderCodex, Enabled: true,
		AccountIDs: []string{"safe-primary", "safe-fallback", "safe-other"},
	}
	svc := New(client, store)

	route, err := svc.PrepareLaunchRoute(context.Background(), "session-1", core.ProviderCodex, "gpt-5")
	if err != nil {
		t.Fatalf("prepare first route: %v", err)
	}
	if route == nil || route.AccountID != "safe-fallback" || route.Token != "opaque-fallback" {
		t.Fatalf("route = %#v", route)
	}

	store.policies[domain.AccountsManagerProviderCodex] = domain.AccountsManagerRoutingPolicy{
		Provider: domain.AccountsManagerProviderCodex, Enabled: false, AccountIDs: []string{"safe-other"},
	}
	client.credentials[1].Unavailable = false
	route, err = svc.PrepareLaunchRoute(context.Background(), "session-1", core.ProviderCodex, "gpt-5")
	if err != nil {
		t.Fatalf("prepare pinned route: %v", err)
	}
	if route.AccountID != "safe-fallback" {
		t.Fatalf("pinned route changed to %q", route.AccountID)
	}
}

func TestPrepareLaunchRouteDoesNotFallbackWhenPinnedAccountBecomesUnavailable(t *testing.T) {
	client := &fakeClient{credentials: []core.CredentialSummary{
		{Ref: "pinned", Provider: core.ProviderClaude, Status: core.CredentialActive, Unavailable: true},
		{Ref: "fallback", Provider: core.ProviderClaude, Status: core.CredentialActive},
	}, stream: make(chan core.OAuthEvent), mintErr: make(map[string]error)}
	store := newFakeRoutingStore()
	store.policies[domain.AccountsManagerProviderClaude] = domain.AccountsManagerRoutingPolicy{Provider: domain.AccountsManagerProviderClaude, Enabled: true, AccountIDs: []string{"safe-pinned", "safe-fallback"}}
	store.routes["session-2:claude"] = domain.AccountsManagerSessionRoute{SessionID: "session-2", Provider: domain.AccountsManagerProviderClaude, AccountID: "safe-pinned"}

	_, err := New(client, store).PrepareLaunchRoute(context.Background(), "session-2", core.ProviderClaude, "claude-sonnet")
	if !errors.Is(err, ErrRoutingAccountUnavailable) {
		t.Fatalf("PrepareLaunchRoute() error = %v", err)
	}
	if len(client.minted) != 0 {
		t.Fatalf("minted fallback route: %v", client.minted)
	}
}

func TestPrepareLaunchRouteDisabledPreservesNativeLaunch(t *testing.T) {
	client := &fakeClient{stream: make(chan core.OAuthEvent), mintErr: make(map[string]error)}
	store := newFakeRoutingStore()
	route, err := New(client, store).PrepareLaunchRoute(context.Background(), "session-3", core.ProviderCodex, "gpt-5")
	if err != nil || route != nil {
		t.Fatalf("disabled route = %#v, err=%v", route, err)
	}
}

func TestSetRoutingPolicyRejectsDuplicateAndWrongProviderAccounts(t *testing.T) {
	client := &fakeClient{credentials: []core.CredentialSummary{
		{Ref: "codex", Provider: core.ProviderCodex, Status: core.CredentialActive},
		{Ref: "claude", Provider: core.ProviderClaude, Status: core.CredentialActive},
	}, stream: make(chan core.OAuthEvent), mintErr: make(map[string]error)}
	service := New(client, newFakeRoutingStore())
	if _, err := service.SetRoutingPolicy(context.Background(), core.ProviderCodex, true, []string{"safe-codex", "safe-codex"}); !errors.Is(err, core.ErrCredentialConflict) {
		t.Fatalf("duplicate policy error = %v", err)
	}
	if _, err := service.SetRoutingPolicy(context.Background(), core.ProviderCodex, true, []string{"safe-claude"}); !errors.Is(err, core.ErrCredentialNotFound) {
		t.Fatalf("wrong-provider policy error = %v", err)
	}
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

func TestServiceRevisionIsSafeForJavaScriptClients(t *testing.T) {
	svc := New(&fakeClient{stream: make(chan core.OAuthEvent)})
	if revision := svc.Snapshot().Revision; revision > 1<<53-1 {
		t.Fatalf("revision = %d, exceeds JavaScript safe integer range", revision)
	}
}

func TestServiceMapsOAuthEventsWithoutExposingState(t *testing.T) {
	fake := &fakeClient{stream: make(chan core.OAuthEvent, 1)}
	svc := New(fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	fake.stream <- core.OAuthEvent{Provider: core.ProviderClaude, Mode: core.OAuthModeCallback, State: "raw-state", Status: core.OAuthCompleted, ExpiresAt: time.Now().Add(time.Minute)}
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
