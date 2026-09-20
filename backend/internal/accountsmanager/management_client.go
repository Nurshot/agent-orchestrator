package accountsmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	managementClientTimeout = 5 * time.Second
	managementResponseLimit = 1 << 20
	managementAPIKeyLimit   = 16 << 10
	managementImportLimit   = 1 << 20
)

var (
	ErrUnavailable          = errors.New("accounts manager unavailable")
	ErrResponseTooLarge     = errors.New("accounts manager response is too large")
	ErrRequestTooLarge      = errors.New("accounts manager request is too large")
	ErrInvalidResponse      = errors.New("accounts manager returned an invalid response")
	ErrUnsupportedProvider  = errors.New("accounts manager provider is unsupported")
	ErrCredentialNotFound   = errors.New("accounts manager credential was not found")
	ErrCredentialConflict   = errors.New("accounts manager credential is ambiguous or already exists")
	ErrInvalidCredential    = errors.New("accounts manager credential is invalid")
	ErrOperationUnsupported = errors.New("accounts manager operation is unsupported")
	ErrOAuthBusy            = errors.New("accounts manager OAuth login is already in progress")
	ErrOAuthExpired         = errors.New("accounts manager OAuth login expired")
)

// ManagementStatusError reports an upstream HTTP status without retaining or
// exposing the response body.
type ManagementStatusError struct {
	Operation  string
	StatusCode int
}

func (e *ManagementStatusError) Error() string {
	return fmt.Sprintf("accounts manager %s failed with status %d", e.Operation, e.StatusCode)
}

type managementTransportError struct {
	operation string
	cause     error
}

func (e *managementTransportError) Error() string {
	return "accounts manager " + e.operation + " request failed"
}

func (e *managementTransportError) Unwrap() error { return e.cause }

type Provider string

const (
	ProviderCodex  Provider = "codex"
	ProviderClaude Provider = "claude"
)

type CredentialSummary struct {
	Ref        string
	Provider   Provider
	Kind       string
	Email      string
	Status     string
	Disabled   bool
	ObservedAt time.Time
}

type RoutingStrategy string

const (
	RoutingRoundRobin         RoutingStrategy = "round-robin"
	RoutingWeightedRoundRobin RoutingStrategy = "weighted-round-robin"
	RoutingFillFirst          RoutingStrategy = "fill-first"
)

type EndpointSource interface {
	Endpoint() (Endpoint, bool)
}

type ManagementClient struct {
	source     EndpointSource
	client     *http.Client
	mutationMu sync.Mutex
}

func NewManagementClient(source EndpointSource, client *http.Client) *ManagementClient {
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	if bounded.Timeout <= 0 || bounded.Timeout > managementClientTimeout {
		bounded.Timeout = managementClientTimeout
	}
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &ManagementClient{source: source, client: &bounded}
}

func (c *ManagementClient) ListCredentials(ctx context.Context) ([]CredentialSummary, error) {
	var payload struct {
		ObservedAt time.Time `json:"observed_at"`
		Files      []struct {
			AuthIndex   string `json:"auth_index"`
			Provider    string `json:"provider"`
			Type        string `json:"type"`
			AccountType string `json:"account_type"`
			Email       string `json:"email"`
			Status      string `json:"status"`
			Disabled    bool   `json:"disabled"`
		} `json:"files"`
	}
	if err := c.doJSON(ctx, "list credentials", http.MethodGet, "/v0/management/auth-files", nil, &payload); err != nil {
		return nil, err
	}

	credentials := make([]CredentialSummary, 0, len(payload.Files))
	for _, file := range payload.Files {
		ref := strings.TrimSpace(file.AuthIndex)
		if ref == "" {
			continue
		}
		providerName := strings.ToLower(strings.TrimSpace(file.Provider))
		if providerName == "" {
			providerName = strings.ToLower(strings.TrimSpace(file.Type))
		}
		provider := Provider(providerName)
		if provider != ProviderCodex && provider != ProviderClaude {
			continue
		}
		credentials = append(credentials, CredentialSummary{
			Ref:        ref,
			Provider:   provider,
			Kind:       strings.TrimSpace(file.AccountType),
			Email:      strings.TrimSpace(file.Email),
			Status:     strings.TrimSpace(file.Status),
			Disabled:   file.Disabled,
			ObservedAt: payload.ObservedAt,
		})
	}
	return credentials, nil
}

func (c *ManagementClient) RoutingStrategy(ctx context.Context) (RoutingStrategy, error) {
	var payload struct {
		Strategy string `json:"strategy"`
	}
	if err := c.doJSON(ctx, "read routing strategy", http.MethodGet, "/v0/management/routing/strategy", nil, &payload); err != nil {
		return "", err
	}
	strategy := RoutingStrategy(strings.TrimSpace(payload.Strategy))
	switch strategy {
	case RoutingRoundRobin, RoutingWeightedRoundRobin, RoutingFillFirst:
		return strategy, nil
	default:
		return "", ErrInvalidResponse
	}
}

func (c *ManagementClient) doJSON(ctx context.Context, operation, method, path string, src, dst any) error {
	if c == nil || c.source == nil || c.client == nil {
		return ErrUnavailable
	}
	endpoint, ready := c.source.Endpoint()
	if !ready || strings.TrimSpace(endpoint.ManagementToken) == "" {
		return ErrUnavailable
	}
	baseURL, ok := verifiedManagementBaseURL(endpoint.BaseURL)
	if !ok {
		return ErrUnavailable
	}

	var requestBody []byte
	var err error
	if src != nil {
		requestBody, err = json.Marshal(src)
		if err != nil {
			return ErrInvalidResponse
		}
		if len(requestBody) > managementImportLimit {
			return ErrRequestTooLarge
		}
	}
	var bodyReader io.Reader
	if requestBody != nil {
		bodyReader = bytes.NewReader(requestBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bodyReader)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+endpoint.ManagementToken)
	req.Header.Set("Accept", "application/json")
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return &managementTransportError{operation: operation, cause: err}
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return &ManagementStatusError{Operation: operation, StatusCode: res.StatusCode}
	}
	if dst == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	mediaType, _, parseErr := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "application/json" {
		return ErrInvalidResponse
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, managementResponseLimit+1))
	if err != nil {
		return &managementTransportError{operation: operation, cause: err}
	}
	if len(body) > managementResponseLimit {
		return ErrResponseTooLarge
	}
	if err = json.Unmarshal(body, dst); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

func verifiedManagementBaseURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return "http://127.0.0.1:" + strconv.Itoa(port), true
}
