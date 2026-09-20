// Package accountsmanager exposes a safe, daemon-owned projection of the
// private Accounts Manager runner.
package accountsmanager

import (
	"context"
	"errors"
	"sync"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/accountsmanager"
)

type Availability string

const (
	AvailabilityStarting Availability = "starting"
	AvailabilityReady    Availability = "ready"
	AvailabilityDegraded Availability = "degraded"
)

type Account struct {
	ID              string
	Provider        core.Provider
	Kind            core.CredentialKind
	Email           string
	Status          core.CredentialState
	Disabled        bool
	Unavailable     bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastRefreshedAt time.Time
	QuotaSupported  bool
	Cooldowns       []core.CredentialCooldown
}

type OAuthSession struct {
	ID               string
	Provider         core.Provider
	Status           core.OAuthState
	FailureCode      string
	AuthorizationURL string
	ExpiresAt        time.Time
	terminalAt       time.Time
}

type Snapshot struct {
	Revision      int64
	Availability  Availability
	Stale         bool
	Accounts      []Account
	OAuthSessions []OAuthSession
}

type Client interface {
	ListCredentials(context.Context) ([]core.CredentialSummary, error)
	CredentialPublicID(string) (string, error)
	OAuthPublicID(string) (string, error)
	StreamOAuthEvents(context.Context, func(core.OAuthEvent) error) error
}

type lifecycleClient interface {
	Client
	StartOAuth(context.Context, core.Provider) (core.OAuthSession, error)
	CancelOAuth(context.Context, string) error
	AddAPIKey(context.Context, core.APIKeyInput) (core.CredentialSummary, error)
	ImportCredential(context.Context, core.CredentialImport) (core.CredentialSummary, error)
	SetCredentialDisabled(context.Context, string, bool) error
	RefreshCredential(context.Context, string) (core.CredentialSummary, error)
	RemoveCredential(context.Context, string) error
	ListCredentialModels(context.Context, string) ([]core.CredentialModel, error)
	FetchCredentialQuota(context.Context, string) (core.CredentialQuota, error)
	ResetCredentialQuota(context.Context, string) error
}

type Service struct {
	client Client

	mu          sync.RWMutex
	snapshot    Snapshot
	rawAccounts map[string]string
	rawOAuth    map[string]string
	subscribers map[chan Snapshot]struct{}
}

func New(client Client) *Service {
	return &Service{
		client:      client,
		snapshot:    Snapshot{Revision: time.Now().UnixNano(), Availability: AvailabilityStarting, Stale: true},
		rawAccounts: make(map[string]string), rawOAuth: make(map[string]string), subscribers: make(map[chan Snapshot]struct{}),
	}
}

func (s *Service) Start(ctx context.Context) {
	if s == nil || s.client == nil {
		return
	}
	go s.watchOAuth(ctx)
}

func (s *Service) watchOAuth(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		err := s.client.StreamOAuthEvents(ctx, func(event core.OAuthEvent) error { s.applyOAuthEvent(ctx, event); return nil })
		if ctx.Err() != nil {
			return
		}
		s.markDegraded()
		if err == nil {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneTerminalLocked(time.Now())
	return cloneSnapshot(s.snapshot)
}

func (s *Service) Refresh(ctx context.Context) (Snapshot, error) {
	credentials, err := s.client.ListCredentials(ctx)
	if err != nil {
		s.markDegraded()
		return s.Snapshot(), err
	}
	accounts := make([]Account, 0, len(credentials))
	raw := make(map[string]string, len(credentials))
	for _, credential := range credentials {
		id, idErr := s.client.CredentialPublicID(credential.Ref)
		if idErr != nil {
			s.markDegraded()
			return s.Snapshot(), idErr
		}
		raw[id] = credential.Ref
		accounts = append(accounts, accountFromCredential(id, credential))
	}
	s.mu.Lock()
	s.rawAccounts = raw
	s.snapshot.Accounts = accounts
	s.snapshot.Availability = AvailabilityReady
	s.snapshot.Stale = false
	s.bumpLocked()
	result := cloneSnapshot(s.snapshot)
	s.mu.Unlock()
	return result, nil
}

func (s *Service) Subscribe(ctx context.Context) <-chan Snapshot {
	updates := make(chan Snapshot, 8)
	s.mu.Lock()
	s.subscribers[updates] = struct{}{}
	updates <- cloneSnapshot(s.snapshot)
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if _, ok := s.subscribers[updates]; ok {
			delete(s.subscribers, updates)
			close(updates)
		}
		s.mu.Unlock()
	}()
	return updates
}

func (s *Service) StartOAuth(ctx context.Context, provider core.Provider) (OAuthSession, error) {
	client, ok := s.client.(lifecycleClient)
	if !ok {
		return OAuthSession{}, core.ErrUnavailable
	}
	session, err := client.StartOAuth(ctx, provider)
	if err != nil {
		return OAuthSession{}, err
	}
	id, err := s.client.OAuthPublicID(session.State)
	if err != nil {
		return OAuthSession{}, err
	}
	public := OAuthSession{ID: id, Provider: session.Provider, Status: core.OAuthPending, AuthorizationURL: session.AuthorizationURL, ExpiresAt: session.ExpiresAt}
	s.mu.Lock()
	s.rawOAuth[id] = session.State
	s.upsertOAuthLocked(public)
	s.bumpLocked()
	s.mu.Unlock()
	return public, nil
}

func (s *Service) CancelOAuth(ctx context.Context, id string) error {
	client, ok := s.client.(lifecycleClient)
	if !ok {
		return core.ErrUnavailable
	}
	s.mu.RLock()
	state, found := s.rawOAuth[id]
	s.mu.RUnlock()
	if !found {
		return nil
	}
	return client.CancelOAuth(ctx, state)
}

func (s *Service) AddAPIKey(ctx context.Context, input core.APIKeyInput) (Snapshot, error) {
	client, ok := s.client.(lifecycleClient)
	if !ok {
		return s.Snapshot(), core.ErrUnavailable
	}
	if _, err := client.AddAPIKey(ctx, input); err != nil {
		return s.Snapshot(), err
	}
	return s.Refresh(ctx)
}

func (s *Service) ImportCredential(ctx context.Context, input core.CredentialImport) (Snapshot, error) {
	client, ok := s.client.(lifecycleClient)
	if !ok {
		return s.Snapshot(), core.ErrUnavailable
	}
	if _, err := client.ImportCredential(ctx, input); err != nil {
		return s.Snapshot(), err
	}
	return s.Refresh(ctx)
}

func (s *Service) SetDisabled(ctx context.Context, id string, disabled bool) (Snapshot, error) {
	client, ref, err := s.resolve(ctx, id)
	if err != nil {
		return s.Snapshot(), err
	}
	if err = client.SetCredentialDisabled(ctx, ref, disabled); err != nil {
		return s.Snapshot(), err
	}
	return s.Refresh(ctx)
}
func (s *Service) RefreshAccount(ctx context.Context, id string) (Snapshot, error) {
	client, ref, err := s.resolve(ctx, id)
	if err != nil {
		return s.Snapshot(), err
	}
	if _, err = client.RefreshCredential(ctx, ref); err != nil {
		return s.Snapshot(), err
	}
	return s.Refresh(ctx)
}
func (s *Service) RemoveAccount(ctx context.Context, id string) (Snapshot, error) {
	client, ref, err := s.resolve(ctx, id)
	if err != nil {
		if errors.Is(err, core.ErrCredentialNotFound) {
			return s.Snapshot(), nil
		}
		return s.Snapshot(), err
	}
	if err = client.RemoveCredential(ctx, ref); err != nil {
		return s.Snapshot(), err
	}
	return s.Refresh(ctx)
}
func (s *Service) Models(ctx context.Context, id string) ([]core.CredentialModel, error) {
	client, ref, err := s.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return client.ListCredentialModels(ctx, ref)
}
func (s *Service) Quota(ctx context.Context, id string) (core.CredentialQuota, error) {
	client, ref, err := s.resolve(ctx, id)
	if err != nil {
		return core.CredentialQuota{}, err
	}
	return client.FetchCredentialQuota(ctx, ref)
}
func (s *Service) ResetQuota(ctx context.Context, id string) error {
	client, ref, err := s.resolve(ctx, id)
	if err != nil {
		return err
	}
	return client.ResetCredentialQuota(ctx, ref)
}

func (s *Service) resolve(ctx context.Context, id string) (lifecycleClient, string, error) {
	client, ok := s.client.(lifecycleClient)
	if !ok {
		return nil, "", core.ErrUnavailable
	}
	if _, err := s.Refresh(ctx); err != nil {
		return nil, "", err
	}
	s.mu.RLock()
	ref, found := s.rawAccounts[id]
	s.mu.RUnlock()
	if !found {
		return nil, "", core.ErrCredentialNotFound
	}
	return client, ref, nil
}

func (s *Service) applyOAuthEvent(ctx context.Context, event core.OAuthEvent) {
	id, err := s.client.OAuthPublicID(event.State)
	if err != nil {
		s.markDegraded()
		return
	}
	public := OAuthSession{ID: id, Provider: event.Provider, Status: event.Status, FailureCode: event.FailureCode, ExpiresAt: event.ExpiresAt}
	if event.Status != core.OAuthPending {
		public.terminalAt = time.Now()
	}
	s.mu.Lock()
	s.rawOAuth[id] = event.State
	s.upsertOAuthLocked(public)
	s.snapshot.Availability = AvailabilityReady
	s.bumpLocked()
	s.mu.Unlock()
	if event.Status != core.OAuthPending {
		go func() {
			timer := time.NewTimer(time.Minute)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.mu.Lock()
			before := len(s.snapshot.OAuthSessions)
			s.pruneTerminalLocked(time.Now())
			if len(s.snapshot.OAuthSessions) != before {
				s.bumpLocked()
			}
			s.mu.Unlock()
		}()
	}
	if event.Status == core.OAuthCompleted {
		_, _ = s.Refresh(ctx)
	}
}

func (s *Service) upsertOAuthLocked(session OAuthSession) {
	for index := range s.snapshot.OAuthSessions {
		if s.snapshot.OAuthSessions[index].ID == session.ID {
			if session.AuthorizationURL == "" {
				session.AuthorizationURL = s.snapshot.OAuthSessions[index].AuthorizationURL
			}
			s.snapshot.OAuthSessions[index] = session
			return
		}
	}
	s.snapshot.OAuthSessions = append(s.snapshot.OAuthSessions, session)
}

func (s *Service) markDegraded() {
	s.mu.Lock()
	s.snapshot.Availability = AvailabilityDegraded
	s.snapshot.Stale = true
	s.bumpLocked()
	s.mu.Unlock()
}
func (s *Service) bumpLocked() {
	s.snapshot.Revision++
	snapshot := cloneSnapshot(s.snapshot)
	for subscriber := range s.subscribers {
		select {
		case subscriber <- snapshot:
		default:
		}
	}
}
func (s *Service) pruneTerminalLocked(now time.Time) {
	kept := s.snapshot.OAuthSessions[:0]
	for _, session := range s.snapshot.OAuthSessions {
		if !session.terminalAt.IsZero() && now.Sub(session.terminalAt) >= time.Minute {
			delete(s.rawOAuth, session.ID)
			continue
		}
		kept = append(kept, session)
	}
	s.snapshot.OAuthSessions = kept
}
func accountFromCredential(id string, value core.CredentialSummary) Account {
	return Account{ID: id, Provider: value.Provider, Kind: value.Kind, Email: value.Email, Status: value.Status, Disabled: value.Disabled, Unavailable: value.Unavailable, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, LastRefreshedAt: value.LastRefreshedAt, QuotaSupported: value.QuotaSupported, Cooldowns: append([]core.CredentialCooldown(nil), value.Cooldowns...)}
}
func cloneSnapshot(value Snapshot) Snapshot {
	value.Accounts = append([]Account(nil), value.Accounts...)
	value.OAuthSessions = append([]OAuthSession(nil), value.OAuthSessions...)
	for i := range value.Accounts {
		value.Accounts[i].Cooldowns = append([]core.CredentialCooldown(nil), value.Accounts[i].Cooldowns...)
	}
	return value
}
