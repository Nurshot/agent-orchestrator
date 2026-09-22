package runner

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

func TestRouteTokenHandlerRequiresManagementKeyAndExistingCredential(t *testing.T) {
	key, _ := hex.DecodeString(strings.Repeat("55", 32))
	capability, _ := newRouteCapability(key)
	handler := newRouteTokenHandler("management-key", "http://127.0.0.1:43127", capability, func(provider, authIndex string) bool {
		return provider == "codex" && authIndex == "auth-index-a"
	})
	body := []byte(`{"provider":"codex","authIndex":"auth-index-a","sessionId":"session-1"}`)

	unauthorized := httptest.NewRequest(http.MethodPost, "/ao/internal/routes/token", bytes.NewReader(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedRecorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/ao/internal/routes/token", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer management-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		BaseURL string `json:"baseUrl"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.BaseURL != "http://127.0.0.1:43127" || !strings.HasPrefix(response.Token, routeTokenPrefix) {
		t.Fatalf("response = %#v", response)
	}

	missingBody := []byte(`{"provider":"codex","authIndex":"missing","sessionId":"session-1"}`)
	missing := httptest.NewRequest(http.MethodPost, "/ao/internal/routes/token", bytes.NewReader(missingBody))
	missing.Header.Set("Authorization", "Bearer management-key")
	missingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(missingRecorder, missing)
	if missingRecorder.Code != http.StatusNotFound || strings.Contains(missingRecorder.Body.String(), "missing") {
		t.Fatalf("missing response = %d %q", missingRecorder.Code, missingRecorder.Body.String())
	}
}

func TestRouteCapabilityAuthenticatesOpaqueTokenAndSelectsExactAccount(t *testing.T) {
	key, _ := hex.DecodeString(strings.Repeat("11", 32))
	capability, err := newRouteCapability(key)
	if err != nil {
		t.Fatal(err)
	}
	token, err := capability.Mint(routeClaims{Provider: "codex", AuthIndex: "auth-index-a", SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "codex") || strings.Contains(token, "auth-index-a") || strings.Contains(token, "session-1") {
		t.Fatalf("route token exposes claims: %q", token)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	result, authErr := capability.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("authenticate: %v", authErr)
	}
	if result.Provider != routeAccessProviderName || result.Principal == token {
		t.Fatalf("unsafe access result: %#v", result)
	}

	selector := newExactRouteSelector(capability, &coreauth.FillFirstSelector{})
	selected, err := selector.Pick(context.Background(), "codex", "gpt-5", coreexecutor.Options{Metadata: map[string]any{
		coreexecutor.CallerScopeMetadataKey: coresession.CallerScope(result.Principal),
	}}, []*coreauth.Auth{
		{ID: "credential-b", Index: "auth-index-b", Provider: "codex", Status: coreauth.StatusActive},
		{ID: "credential-a", Index: "auth-index-a", Provider: "codex", Status: coreauth.StatusActive},
	})
	if err != nil {
		t.Fatalf("pick exact route: %v", err)
	}
	if selected.Index != "auth-index-a" {
		t.Fatalf("selected auth index = %q", selected.Index)
	}
}

func TestExactRouteSelectorNeverFallsBackForPinnedRequest(t *testing.T) {
	key, _ := hex.DecodeString(strings.Repeat("22", 32))
	capability, _ := newRouteCapability(key)
	token, _ := capability.Mint(routeClaims{Provider: "claude", AuthIndex: "missing-auth", SessionID: "session-2"})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/messages", nil)
	req.Header.Set("x-api-key", token)
	result, authErr := capability.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatal(authErr)
	}
	selector := newExactRouteSelector(capability, &coreauth.FillFirstSelector{})
	_, err := selector.Pick(context.Background(), "claude", "claude-sonnet", coreexecutor.Options{Metadata: map[string]any{
		coreexecutor.CallerScopeMetadataKey: coresession.CallerScope(result.Principal),
	}}, []*coreauth.Auth{{ID: "fallback", Index: "fallback-index", Provider: "claude", Status: coreauth.StatusActive}})
	if err == nil || !strings.Contains(err.Error(), "pinned account is unavailable") {
		t.Fatalf("Pick() error = %v", err)
	}
}

func TestExactRouteSelectorRejectsDisabledPinnedAccount(t *testing.T) {
	key, _ := hex.DecodeString(strings.Repeat("77", 32))
	capability, _ := newRouteCapability(key)
	token, _ := capability.Mint(routeClaims{Provider: "codex", AuthIndex: "pinned", SessionID: "session-1"})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	result, authErr := capability.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatal(authErr)
	}
	selector := newExactRouteSelector(capability, &coreauth.FillFirstSelector{})
	_, err := selector.Pick(context.Background(), "codex", "gpt-5", coreexecutor.Options{Metadata: map[string]any{
		coreexecutor.CallerScopeMetadataKey: coresession.CallerScope(result.Principal),
	}}, []*coreauth.Auth{
		{Index: "pinned", Provider: "codex", Status: coreauth.StatusActive, Disabled: true},
		{Index: "fallback", Provider: "codex", Status: coreauth.StatusActive},
	})
	if err == nil || !strings.Contains(err.Error(), "pinned account is unavailable") {
		t.Fatalf("Pick() error = %v", err)
	}
}

func TestRouteCapabilitySurvivesRestartWithSameKey(t *testing.T) {
	key, _ := hex.DecodeString(strings.Repeat("33", 32))
	first, _ := newRouteCapability(key)
	token, _ := first.Mint(routeClaims{Provider: "codex", AuthIndex: "auth-index", SessionID: "session"})
	second, _ := newRouteCapability(key)
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if _, authErr := second.Authenticate(context.Background(), req); authErr != nil {
		t.Fatalf("authenticate after restart: %v", authErr)
	}
}

func TestRouteCapabilityIgnoresNormalClientCredentials(t *testing.T) {
	key, _ := hex.DecodeString(strings.Repeat("44", 32))
	capability, _ := newRouteCapability(key)
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	req.Header.Set("Authorization", "Bearer normal-client-key")
	if _, authErr := capability.Authenticate(context.Background(), req); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNotHandled) {
		t.Fatalf("Authenticate() error = %v, want not handled", authErr)
	}
}
