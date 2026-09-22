package domain

import "time"

// AccountsManagerProvider identifies a provider supported by AO's embedded
// Accounts Manager. It is intentionally narrower than AgentHarness.
type AccountsManagerProvider string

const (
	AccountsManagerProviderCodex  AccountsManagerProvider = "codex"
	AccountsManagerProviderClaude AccountsManagerProvider = "claude"
)

func (p AccountsManagerProvider) Valid() bool {
	return p == AccountsManagerProviderCodex || p == AccountsManagerProviderClaude
}

// AccountsManagerRoutingPolicy is AO's ordered account preference for new
// sessions. AccountIDs are public-safe identifiers, never engine auth refs.
type AccountsManagerRoutingPolicy struct {
	Provider   AccountsManagerProvider
	Enabled    bool
	AccountIDs []string
}

// AccountsManagerSessionRoute pins one provider in one AO session to a single
// public-safe account identifier.
type AccountsManagerSessionRoute struct {
	SessionID SessionID
	Provider  AccountsManagerProvider
	AccountID string
	CreatedAt time.Time
	UpdatedAt time.Time
}
