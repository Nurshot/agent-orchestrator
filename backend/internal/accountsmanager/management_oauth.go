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
type OAuthMode string

const (
	OAuthPending      OAuthState = "pending"
	OAuthCompleted    OAuthState = "completed"
	OAuthFailed       OAuthState = "failed"
	OAuthExpired      OAuthState = "expired"
	OAuthModeCallback OAuthMode  = "callback"
	OAuthModeDevice   OAuthMode  = "device"
)

type OAuthSession struct {
	Provider         Provider
	Mode             OAuthMode
	State            string
	AuthorizationURL string
	UserCode         string
	ExpiresAt        time.Time
}

type OAuthStatus struct {
	State       OAuthState
	FailureCode string
}

func (c *ManagementClient) StartOAuth(ctx context.Context, provider Provider, mode OAuthMode) (OAuthSession, error) {
	if !validProvider(provider) {
		return OAuthSession{}, ErrUnsupportedProvider
	}
	if mode == "" {
		mode = OAuthModeCallback
	}
	if mode != OAuthModeCallback && !(provider == ProviderCodex && mode == OAuthModeDevice) {
		return OAuthSession{}, ErrOperationUnsupported
	}
	var response struct {
		Provider         string    `json:"provider"`
		Mode             string    `json:"mode"`
		State            string    `json:"state"`
		AuthorizationURL string    `json:"authorizationUrl"`
		UserCode         string    `json:"userCode"`
		ExpiresAt        time.Time `json:"expiresAt"`
	}
	err := c.doJSON(ctx, "start OAuth", http.MethodPost, "/ao/internal/oauth/start", map[string]string{"provider": string(provider), "mode": string(mode)}, &response)
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
	responseMode := OAuthMode(strings.ToLower(strings.TrimSpace(response.Mode)))
	userCode := strings.TrimSpace(response.UserCode)
	if parseErr != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil || state == "" || responseProvider != provider || responseMode != mode || response.ExpiresAt.IsZero() || (mode == OAuthModeDevice && userCode == "") || len(userCode) > 128 {
		return OAuthSession{}, ErrInvalidResponse
	}
	return OAuthSession{
		Provider:         responseProvider,
		Mode:             responseMode,
		State:            state,
		AuthorizationURL: parsedURL.String(),
		UserCode:         userCode,
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
