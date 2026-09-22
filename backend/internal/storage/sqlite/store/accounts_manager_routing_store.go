package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func (s *Store) PutAccountsManagerRoutingPolicy(ctx context.Context, policy domain.AccountsManagerRoutingPolicy) error {
	if !policy.Provider.Valid() {
		return fmt.Errorf("put accounts manager routing policy: invalid provider")
	}
	seen := make(map[string]struct{}, len(policy.AccountIDs))
	for _, id := range policy.AccountIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("put accounts manager routing policy: empty account id")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("put accounts manager routing policy: duplicate account id")
		}
		seen[id] = struct{}{}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "put accounts manager routing policy", func(q *gen.Queries) error {
		enabled := int64(0)
		if policy.Enabled {
			enabled = 1
		}
		if err := q.UpsertAccountsManagerRoutingPolicy(ctx, gen.UpsertAccountsManagerRoutingPolicyParams{
			Provider: string(policy.Provider), Enabled: enabled, UpdatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		if err := q.DeleteAccountsManagerRoutingPolicyAccounts(ctx, string(policy.Provider)); err != nil {
			return err
		}
		for position, id := range policy.AccountIDs {
			if err := q.InsertAccountsManagerRoutingPolicyAccount(ctx, gen.InsertAccountsManagerRoutingPolicyAccountParams{
				Provider: string(policy.Provider), AccountID: strings.TrimSpace(id), Position: int64(position),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetAccountsManagerRoutingPolicy(ctx context.Context, provider domain.AccountsManagerProvider) (domain.AccountsManagerRoutingPolicy, error) {
	if !provider.Valid() {
		return domain.AccountsManagerRoutingPolicy{}, fmt.Errorf("get accounts manager routing policy: invalid provider")
	}
	row, err := s.qr.GetAccountsManagerRoutingPolicy(ctx, string(provider))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccountsManagerRoutingPolicy{Provider: provider, AccountIDs: []string{}}, nil
	}
	if err != nil {
		return domain.AccountsManagerRoutingPolicy{}, fmt.Errorf("get accounts manager routing policy: %w", err)
	}
	ids, err := s.qr.ListAccountsManagerRoutingPolicyAccounts(ctx, string(provider))
	if err != nil {
		return domain.AccountsManagerRoutingPolicy{}, fmt.Errorf("list accounts manager routing policy accounts: %w", err)
	}
	return domain.AccountsManagerRoutingPolicy{Provider: provider, Enabled: row.Enabled != 0, AccountIDs: ids}, nil
}

func (s *Store) GetOrCreateAccountsManagerSessionRoute(ctx context.Context, route domain.AccountsManagerSessionRoute) (domain.AccountsManagerSessionRoute, bool, error) {
	if route.SessionID == "" || !route.Provider.Valid() || strings.TrimSpace(route.AccountID) == "" {
		return domain.AccountsManagerSessionRoute{}, false, fmt.Errorf("create accounts manager session route: invalid route")
	}
	if route.CreatedAt.IsZero() {
		route.CreatedAt = time.Now().UTC()
	}
	if route.UpdatedAt.IsZero() {
		route.UpdatedAt = route.CreatedAt
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.InsertAccountsManagerSessionRoute(ctx, gen.InsertAccountsManagerSessionRouteParams{
		SessionID: string(route.SessionID), Provider: string(route.Provider), AccountID: strings.TrimSpace(route.AccountID), CreatedAt: route.CreatedAt, UpdatedAt: route.UpdatedAt,
	})
	if err != nil {
		return domain.AccountsManagerSessionRoute{}, false, fmt.Errorf("insert accounts manager session route: %w", err)
	}
	got, err := s.getAccountsManagerSessionRoute(ctx, s.qw, route.SessionID, route.Provider)
	return got, rows == 1, err
}

func (s *Store) GetAccountsManagerSessionRoute(ctx context.Context, sessionID domain.SessionID, provider domain.AccountsManagerProvider) (domain.AccountsManagerSessionRoute, bool, error) {
	route, err := s.getAccountsManagerSessionRoute(ctx, s.qr, sessionID, provider)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccountsManagerSessionRoute{}, false, nil
	}
	return route, err == nil, err
}

func (s *Store) getAccountsManagerSessionRoute(ctx context.Context, q *gen.Queries, sessionID domain.SessionID, provider domain.AccountsManagerProvider) (domain.AccountsManagerSessionRoute, error) {
	row, err := q.GetAccountsManagerSessionRoute(ctx, gen.GetAccountsManagerSessionRouteParams{SessionID: string(sessionID), Provider: string(provider)})
	if err != nil {
		return domain.AccountsManagerSessionRoute{}, err
	}
	return domain.AccountsManagerSessionRoute{SessionID: domain.SessionID(row.SessionID), Provider: domain.AccountsManagerProvider(row.Provider), AccountID: row.AccountID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}
