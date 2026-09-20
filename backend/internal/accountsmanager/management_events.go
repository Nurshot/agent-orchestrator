package accountsmanager

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"
)

type OAuthEvent struct {
	Provider    Provider
	State       string
	Status      OAuthState
	FailureCode string
	ExpiresAt   time.Time
}

// StreamOAuthEvents blocks until the authenticated runner stream closes or
// ctx is cancelled. Heartbeats are ignored and only validated safe events are
// passed to consume.
func (c *ManagementClient) StreamOAuthEvents(ctx context.Context, consume func(OAuthEvent) error) error {
	if c == nil || c.source == nil || c.client == nil || consume == nil {
		return ErrUnavailable
	}
	endpoint, ready := c.source.Endpoint()
	baseURL, ok := verifiedManagementBaseURL(endpoint.BaseURL)
	if !ready || !ok || strings.TrimSpace(endpoint.ManagementToken) == "" {
		return ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/ao/internal/oauth/events", nil)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+endpoint.ManagementToken)
	req.Header.Set("Accept", "text/event-stream")
	streamClient := *c.client
	streamClient.Timeout = 0
	res, err := streamClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return &managementTransportError{operation: "stream OAuth events", cause: err}
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return &ManagementStatusError{Operation: "stream OAuth events", StatusCode: res.StatusCode}
	}
	mediaType, _, parseErr := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "text/event-stream" {
		return ErrInvalidResponse
	}

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), managementResponseLimit)
	eventType := ""
	data := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if eventType == "oauth_session" && data != "" {
				event, decodeErr := decodeOAuthEvent([]byte(data))
				if decodeErr != nil {
					return decodeErr
				}
				if err = consume(event); err != nil {
					return err
				}
			}
			eventType, data = "", ""
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			part := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				data = part
			} else {
				data += "\n" + part
			}
		}
	}
	if err = scanner.Err(); err != nil {
		return &managementTransportError{operation: "stream OAuth events", cause: err}
	}
	return nil
}

func decodeOAuthEvent(data []byte) (OAuthEvent, error) {
	var raw struct {
		Provider    string    `json:"provider"`
		State       string    `json:"state"`
		Status      string    `json:"status"`
		FailureCode string    `json:"failureCode"`
		ExpiresAt   time.Time `json:"expiresAt"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return OAuthEvent{}, ErrInvalidResponse
	}
	event := OAuthEvent{Provider: Provider(strings.TrimSpace(raw.Provider)), State: strings.TrimSpace(raw.State), Status: OAuthState(strings.TrimSpace(raw.Status)), FailureCode: strings.TrimSpace(raw.FailureCode), ExpiresAt: raw.ExpiresAt}
	if !validProvider(event.Provider) || event.State == "" || len(event.State) > 256 || event.ExpiresAt.IsZero() {
		return OAuthEvent{}, ErrInvalidResponse
	}
	switch event.Status {
	case OAuthPending, OAuthCompleted, OAuthFailed, OAuthExpired:
	default:
		return OAuthEvent{}, ErrInvalidResponse
	}
	if event.Status == OAuthFailed {
		event.FailureCode = "authentication_failed"
	} else if event.Status == OAuthExpired && event.FailureCode != "cancelled" {
		event.FailureCode = "expired"
	} else if event.Status != OAuthExpired {
		event.FailureCode = ""
	}
	return event, nil
}

func (c *ManagementClient) CredentialPublicID(ref string) (string, error) {
	return c.publicID("account", ref, "amc_")
}

func (c *ManagementClient) OAuthPublicID(state string) (string, error) {
	return c.publicID("oauth", state, "amo_")
}

func (c *ManagementClient) publicID(namespace, value, prefix string) (string, error) {
	if c == nil || c.source == nil || strings.TrimSpace(value) == "" {
		return "", ErrUnavailable
	}
	endpoint, ready := c.source.Endpoint()
	if !ready || strings.TrimSpace(endpoint.ManagementToken) == "" {
		return "", ErrUnavailable
	}
	mac := hmac.New(sha256.New, []byte(endpoint.ManagementToken))
	_, _ = mac.Write([]byte(namespace + "\x00" + value))
	return prefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:18]), nil
}
