package accountsmanager

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OAuthState string

const (
	OAuthPending   OAuthState = "pending"
	OAuthCompleted OAuthState = "completed"
	OAuthFailed    OAuthState = "failed"
	OAuthExpired   OAuthState = "expired"
)

type OAuthSession struct {
	Provider         Provider
	State            string
	AuthorizationURL string
	ExpiresAt        time.Time
}

type OAuthStatus struct {
	State       OAuthState
	FailureCode string
}

func (c *ManagementClient) StartOAuth(ctx context.Context, provider Provider) (OAuthSession, error) {
	if !validProvider(provider) {
		return OAuthSession{}, ErrUnsupportedProvider
	}
	var response struct {
		Provider         string    `json:"provider"`
		State            string    `json:"state"`
		AuthorizationURL string    `json:"authorizationUrl"`
		ExpiresAt        time.Time `json:"expiresAt"`
	}
	err := c.doJSON(ctx, "start OAuth", http.MethodPost, "/ao/internal/oauth/start", map[string]Provider{"provider": provider}, &response)
	if err != nil {
		var statusErr *ManagementStatusError
		if errors.As(err, &statusErr) {
			switch statusErr.StatusCode {
			case http.StatusBadRequest:
				return OAuthSession{}, ErrUnsupportedProvider
			case http.StatusConflict:
				return OAuthSession{}, ErrOAuthBusy
			}
		}
		return OAuthSession{}, err
	}
	parsedURL, parseErr := url.Parse(strings.TrimSpace(response.AuthorizationURL))
	state := strings.TrimSpace(response.State)
	responseProvider := Provider(strings.ToLower(strings.TrimSpace(response.Provider)))
	if parseErr != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil || state == "" || responseProvider != provider || response.ExpiresAt.IsZero() {
		return OAuthSession{}, ErrInvalidResponse
	}
	return OAuthSession{
		Provider:         responseProvider,
		State:            state,
		AuthorizationURL: parsedURL.String(),
		ExpiresAt:        response.ExpiresAt,
	}, nil
}

func (c *ManagementClient) GetOAuthStatus(ctx context.Context, state string) (OAuthStatus, error) {
	state = strings.TrimSpace(state)
	if state == "" || len(state) > 256 {
		return OAuthStatus{}, ErrOAuthExpired
	}
	query := url.Values{"state": []string{state}}
	var response struct {
		Status string `json:"status"`
	}
	if err := c.doJSON(ctx, "read OAuth status", http.MethodGet, "/ao/internal/oauth/status?"+query.Encode(), nil, &response); err != nil {
		return OAuthStatus{}, err
	}
	switch OAuthState(strings.TrimSpace(response.Status)) {
	case OAuthPending:
		return OAuthStatus{State: OAuthPending}, nil
	case OAuthCompleted:
		return OAuthStatus{State: OAuthCompleted}, nil
	case OAuthFailed:
		return OAuthStatus{State: OAuthFailed, FailureCode: "authentication_failed"}, nil
	case OAuthExpired:
		return OAuthStatus{State: OAuthExpired}, ErrOAuthExpired
	default:
		return OAuthStatus{}, ErrInvalidResponse
	}
}

func (c *ManagementClient) CancelOAuth(ctx context.Context, state string) error {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil
	}
	if len(state) > 256 {
		return ErrOAuthExpired
	}
	query := url.Values{"state": []string{state}}
	return c.doJSON(ctx, "cancel OAuth", http.MethodDelete, "/ao/internal/oauth/session?"+query.Encode(), nil, nil)
}

func validProvider(provider Provider) bool {
	return provider == ProviderCodex || provider == ProviderClaude
}
