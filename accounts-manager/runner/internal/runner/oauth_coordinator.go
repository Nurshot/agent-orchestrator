package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	oauthSessionDuration = 5 * time.Minute
	oauthQueryLimit      = 16 << 10
	oauthResponseLimit   = 1 << 20
)

type oauthProviderConfig struct {
	upstreamPath string
	callbackAddr string
	callbackPath string
}

var oauthProviders = map[string]oauthProviderConfig{
	"codex": {
		upstreamPath: "/v0/management/codex-auth-url",
		callbackAddr: "127.0.0.1:1455",
		callbackPath: "/auth/callback",
	},
	"claude": {
		upstreamPath: "/v0/management/anthropic-auth-url",
		callbackAddr: "127.0.0.1:54545",
		callbackPath: "/callback",
	},
}

type oauthRunnerSession struct {
	provider         string
	state            string
	authorizationURL string
	expiresAt        time.Time
	status           string
	listener         net.Listener
}

type oauthCoordinator struct {
	baseURL       string
	managementKey string
	client        *http.Client
	listen        func(string, string) (net.Listener, error)

	mu        sync.Mutex
	sessions  map[string]*oauthRunnerSession
	providers map[string]string
	closed    bool
}

func newOAuthCoordinator(baseURL, managementKey string, client *http.Client, listen func(string, string) (net.Listener, error)) *oauthCoordinator {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if listen == nil {
		listen = net.Listen
	}
	return &oauthCoordinator{
		baseURL:       strings.TrimRight(baseURL, "/"),
		managementKey: managementKey,
		client:        client,
		listen:        listen,
		sessions:      make(map[string]*oauthRunnerSession),
		providers:     make(map[string]string),
	}
}

func (c *oauthCoordinator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !validControlAuthorization(r.Header.Get("Authorization"), c.managementKey) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/ao/internal/oauth/start":
		c.handleStart(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/ao/internal/oauth/status":
		c.handleStatus(w, r)
	case r.Method == http.MethodDelete && r.URL.Path == "/ao/internal/oauth/session":
		c.handleCancel(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (c *oauthCoordinator) handleStart(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
	}
	if err := decodeBoundedJSON(r.Body, &input); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	config, ok := oauthProviders[provider]
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_provider")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		writeOAuthError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if state := c.providers[provider]; state != "" {
		if existing := c.sessions[state]; existing != nil && existing.status == "pending" && time.Now().Before(existing.expiresAt) {
			writeOAuthSession(w, existing)
			return
		}
		delete(c.providers, provider)
	}

	listener, err := c.listen("tcp", config.callbackAddr)
	if err != nil {
		writeOAuthError(w, http.StatusConflict, "callback_unavailable")
		return
	}

	var upstream struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err = c.upstreamJSON(r.Context(), http.MethodGet, config.upstreamPath, nil, &upstream); err != nil {
		_ = listener.Close()
		writeOAuthError(w, http.StatusBadGateway, "oauth_start_failed")
		return
	}
	authorizationURL, valid := validAuthorizationURL(upstream.URL)
	state := strings.TrimSpace(upstream.State)
	if upstream.Status != "ok" || !valid || state == "" || len(state) > 256 {
		_ = listener.Close()
		writeOAuthError(w, http.StatusBadGateway, "invalid_upstream_response")
		return
	}
	if _, exists := c.sessions[state]; exists {
		_ = listener.Close()
		writeOAuthError(w, http.StatusBadGateway, "invalid_upstream_response")
		return
	}

	session := &oauthRunnerSession{
		provider:         provider,
		state:            state,
		authorizationURL: authorizationURL,
		expiresAt:        time.Now().Add(oauthSessionDuration),
		status:           "pending",
		listener:         listener,
	}
	c.sessions[state] = session
	c.providers[provider] = state

	server := &http.Server{
		Handler:           c.callbackHandler(session),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()
	go c.expireSession(state, session.expiresAt)
	writeOAuthSession(w, session)
}

func (c *oauthCoordinator) handleStatus(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" || len(state) > 256 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_state")
		return
	}

	c.mu.Lock()
	session := c.sessions[state]
	if session == nil {
		c.mu.Unlock()
		writeOAuthStatus(w, "expired")
		return
	}
	if !time.Now().Before(session.expiresAt) || session.status == "expired" {
		c.expireSessionLocked(session)
		c.mu.Unlock()
		writeOAuthStatus(w, "expired")
		return
	}
	if session.status != "pending" {
		status := session.status
		c.mu.Unlock()
		writeOAuthStatus(w, status)
		return
	}
	c.mu.Unlock()

	query := url.Values{"state": []string{state}}
	var upstream struct {
		Status string `json:"status"`
	}
	if err := c.upstreamJSON(r.Context(), http.MethodGet, "/v0/management/get-auth-status?"+query.Encode(), nil, &upstream); err != nil {
		writeOAuthError(w, http.StatusBadGateway, "oauth_status_failed")
		return
	}
	status := "failed"
	switch upstream.Status {
	case "wait":
		status = "pending"
	case "ok":
		status = "completed"
	case "error":
		status = "failed"
	}
	if status != "pending" {
		c.mu.Lock()
		if current := c.sessions[state]; current == session {
			current.status = status
			c.closeListenerLocked(current)
			delete(c.providers, current.provider)
		}
		c.mu.Unlock()
	}
	writeOAuthStatus(w, status)
}

func (c *oauthCoordinator) handleCancel(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" || len(state) > 256 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_state")
		return
	}
	c.mu.Lock()
	session := c.sessions[state]
	if session == nil {
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	c.mu.Unlock()

	query := url.Values{"state": []string{state}}
	var ignored map[string]any
	if err := c.upstreamJSON(r.Context(), http.MethodDelete, "/v0/management/oauth-session?"+query.Encode(), nil, &ignored); err != nil {
		writeOAuthError(w, http.StatusBadGateway, "oauth_cancel_failed")
		return
	}
	c.mu.Lock()
	if current := c.sessions[state]; current == session {
		c.closeListenerLocked(current)
		delete(c.sessions, state)
		delete(c.providers, current.provider)
	}
	c.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (c *oauthCoordinator) callbackHandler(session *oauthRunnerSession) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL == nil {
			writeOAuthBrowserPage(w, http.StatusNotFound, false)
			return
		}
		config := oauthProviders[session.provider]
		if r.URL.Path != config.callbackPath || len(r.URL.RawQuery) > oauthQueryLimit {
			writeOAuthBrowserPage(w, http.StatusBadRequest, false)
			return
		}
		state := strings.TrimSpace(r.URL.Query().Get("state"))
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		errorCode := strings.TrimSpace(r.URL.Query().Get("error"))
		if state != session.state || (code == "" && errorCode == "") {
			writeOAuthBrowserPage(w, http.StatusBadRequest, false)
			return
		}
		c.mu.Lock()
		current := c.sessions[state]
		active := current == session && current.status == "pending" && time.Now().Before(current.expiresAt)
		c.mu.Unlock()
		if !active {
			writeOAuthBrowserPage(w, http.StatusGone, false)
			return
		}
		payload := map[string]string{"state": state}
		if code != "" {
			payload["code"] = code
		} else {
			payload["error"] = errorCode
		}
		var upstream map[string]any
		if err := c.upstreamJSON(r.Context(), http.MethodPost, "/v0/management/oauth-callback", payload, &upstream); err != nil {
			writeOAuthBrowserPage(w, http.StatusBadGateway, false)
			return
		}
		c.mu.Lock()
		c.closeListenerLocked(session)
		c.mu.Unlock()
		writeOAuthBrowserPage(w, http.StatusOK, true)
	})
}

func (c *oauthCoordinator) upstreamJSON(ctx context.Context, method, path string, src, dst any) error {
	var body io.Reader
	if src != nil {
		encoded, err := json.Marshal(src)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.managementKey)
	req.Header.Set("Accept", "application/json")
	if src != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("upstream request failed")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return errors.New("invalid upstream response")
	}
	limited, err := io.ReadAll(io.LimitReader(response.Body, oauthResponseLimit+1))
	if err != nil || len(limited) > oauthResponseLimit {
		return errors.New("invalid upstream response")
	}
	if dst != nil && json.Unmarshal(limited, dst) != nil {
		return errors.New("invalid upstream response")
	}
	return nil
}

func (c *oauthCoordinator) expireSession(state string, expiresAt time.Time) {
	timer := time.NewTimer(time.Until(expiresAt))
	defer timer.Stop()
	<-timer.C
	c.mu.Lock()
	defer c.mu.Unlock()
	if session := c.sessions[state]; session != nil && session.expiresAt.Equal(expiresAt) && session.status == "pending" {
		c.expireSessionLocked(session)
	}
}

func (c *oauthCoordinator) expireSessionLocked(session *oauthRunnerSession) {
	session.status = "expired"
	c.closeListenerLocked(session)
	delete(c.providers, session.provider)
}

func (c *oauthCoordinator) closeListenerLocked(session *oauthRunnerSession) {
	if session != nil && session.listener != nil {
		_ = session.listener.Close()
		session.listener = nil
	}
}

func (c *oauthCoordinator) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, session := range c.sessions {
		c.closeListenerLocked(session)
	}
}

func decodeBoundedJSON(body io.Reader, dst any) error {
	data, err := io.ReadAll(io.LimitReader(body, oauthResponseLimit+1))
	if err != nil || len(data) > oauthResponseLimit {
		return errors.New("invalid JSON")
	}
	return json.Unmarshal(data, dst)
}

func validAuthorizationURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	return parsed.String(), true
}

func writeOAuthSession(w http.ResponseWriter, session *oauthRunnerSession) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"provider":         session.provider,
		"state":            session.state,
		"authorizationUrl": session.authorizationURL,
		"expiresAt":        session.expiresAt,
	})
}

func writeOAuthStatus(w http.ResponseWriter, status string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func writeOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeOAuthBrowserPage(w http.ResponseWriter, status int, success bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if success {
		_, _ = io.WriteString(w, "<!doctype html><title>Sign-in complete</title><p>Sign-in complete. You can close this window.</p>")
		return
	}
	_, _ = io.WriteString(w, "<!doctype html><title>Sign-in failed</title><p>Sign-in could not be completed. Return to AO and try again.</p>")
}
