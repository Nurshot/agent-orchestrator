package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/accountsmanager"
)

type fakeAccountsManagerStatus struct {
	status   accountsmanager.Status
	endpoint accountsmanager.Endpoint
}

func (f fakeAccountsManagerStatus) Status() accountsmanager.Status { return f.status }

func TestAccountsManagerStatusResponseIsRedacted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status accountsmanager.Status
		state  string
		reason any
	}{
		{
			name:   "ready",
			status: accountsmanager.Status{State: accountsmanager.StateReady, EngineVersion: "v7.3.8"},
			state:  "ready",
			reason: nil,
		},
		{
			name:   "degraded",
			status: accountsmanager.Status{State: accountsmanager.StateDegraded, Reason: accountsmanager.ReasonBinaryMissing},
			state:  "degraded",
			reason: "binary_missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := chi.NewRouter()
			privateEndpoint := accountsmanager.Endpoint{
				BaseURL:         "http://127.0.0.1:54321",
				ClientToken:     "recognizable-client-token",
				ManagementToken: "recognizable-management-token",
			}
			controller := AccountsManagerController{Status: fakeAccountsManagerStatus{status: tt.status, endpoint: privateEndpoint}}
			controller.Register(router)
			request := httptest.NewRequest(http.MethodGet, "/accounts-manager/status", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["state"] != tt.state || body["reason"] != tt.reason {
				t.Fatalf("body = %#v", body)
			}
			for _, forbidden := range []string{"url", "port", "pid", "token", "auth", "path", "error"} {
				if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
					t.Fatalf("response exposed forbidden word %q: %s", forbidden, response.Body.String())
				}
			}
			for _, secret := range []string{privateEndpoint.BaseURL, privateEndpoint.ClientToken, privateEndpoint.ManagementToken} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("response exposed private endpoint material")
				}
			}
		})
	}
}
