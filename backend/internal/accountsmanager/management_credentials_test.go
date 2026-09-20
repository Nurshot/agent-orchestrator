package accountsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestListCredentialsProjectsSafeLifecycleAndCooldowns(t *testing.T) {
	t.Parallel()

	const secret = "private-upstream-secret"
	response := `{
		"observed_at":"2026-09-20T10:11:12Z",
		"files":[{
			"auth_index":"codex-ref","provider":"codex","account_type":"oauth","email":"user@example.com",
			"status":"error","disabled":false,"unavailable":true,"supports_quota":true,
			"created_at":"2026-09-18T10:00:00Z","updated_at":"2026-09-19T10:00:00Z","last_refresh":"2026-09-20T09:00:00Z",
			"cooldowns":[{"scope":"model","model_key":"gpt-test","reason":"quota","retry_at":"2026-09-20T11:00:00Z","remaining_seconds":120,"http_status":429,"message":"` + secret + `"}],
			"path":"/private/file.json","account":"` + secret + `","id_token":{"email":"hidden@example.com"}
		},{"auth_index":"other","provider":"gemini","status":"active"}]
	}`
	client := managementTestClient(func(req *http.Request) (*http.Response, error) {
		return managementJSONResponse(req, http.StatusOK, response), nil
	})
	got, err := client.ListCredentials(context.Background())
	if err != nil {
		t.Fatalf("ListCredentials() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("credentials = %#v", got)
	}
	credential := got[0]
	if credential.Ref != "codex-ref" || credential.Status != CredentialError || !credential.Unavailable || !credential.QuotaSupported || len(credential.Cooldowns) != 1 {
		t.Fatalf("credential = %#v", credential)
	}
	if cooldown := credential.Cooldowns[0]; cooldown.Model != "gpt-test" || cooldown.HTTPStatus != 429 || cooldown.RemainingSeconds != 120 {
		t.Fatalf("cooldown = %#v", cooldown)
	}
	encoded, _ := json.Marshal(got)
	for _, private := range []string{secret, "/private/file.json", "hidden@example.com"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("summary exposed %q", private)
		}
	}
}

func TestCredentialMutationsResolveUniqueReference(t *testing.T) {
	t.Parallel()

	var disabled atomic.Bool
	var refreshed atomic.Bool
	client := managementTestClient(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v0/management/auth-files":
			status := "active"
			if refreshed.Load() {
				status = "refreshing"
			}
			return managementJSONResponse(req, http.StatusOK, `{"files":[{"auth_index":"target-ref","name":"target.json","provider":"claude","account_type":"oauth","status":"`+status+`","disabled":`+boolJSON(disabled.Load())+`}]}`), nil
		case req.Method == http.MethodPatch && req.URL.Path == "/v0/management/auth-files/status":
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"name":"target.json"`) || !strings.Contains(string(body), `"auth_index":"target-ref"`) {
				t.Fatalf("disable body = %s", body)
			}
			disabled.Store(true)
			return managementJSONResponse(req, http.StatusOK, `{"status":"ok"}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v0/management/auth-files/refresh":
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"name":"target.json"`) {
				t.Fatalf("refresh body = %s", body)
			}
			refreshed.Store(true)
			return managementJSONResponse(req, http.StatusOK, `{"ok":true}`), nil
		default:
			return managementJSONResponse(req, http.StatusNotFound, `{}`), nil
		}
	})
	if err := client.SetCredentialDisabled(context.Background(), "target-ref", true); err != nil {
		t.Fatalf("SetCredentialDisabled() error = %v", err)
	}
	refreshedCredential, err := client.RefreshCredential(context.Background(), "target-ref")
	if err != nil {
		t.Fatalf("RefreshCredential() error = %v", err)
	}
	if refreshedCredential.Status != CredentialDisabled {
		t.Fatalf("RefreshCredential() = %#v", refreshedCredential)
	}
}

func TestRemoveCredentialHandlesFilesAPIKeysAndMissing(t *testing.T) {
	t.Parallel()

	t.Run("OAuth file", func(t *testing.T) {
		var deleted atomic.Bool
		client := managementTestClient(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodGet && req.URL.Path == "/v0/management/auth-files":
				if deleted.Load() {
					return managementJSONResponse(req, http.StatusOK, `{"files":[]}`), nil
				}
				return managementJSONResponse(req, http.StatusOK, `{"files":[{"auth_index":"oauth-ref","name":"oauth.json","provider":"codex","account_type":"oauth","status":"active"}]}`), nil
			case req.Method == http.MethodDelete && req.URL.Path == "/v0/management/auth-files":
				if req.URL.Query().Get("name") != "oauth.json" {
					t.Fatalf("delete name = %q", req.URL.Query().Get("name"))
				}
				deleted.Store(true)
				return managementJSONResponse(req, http.StatusOK, `{"status":"ok"}`), nil
			default:
				return managementJSONResponse(req, http.StatusNotFound, `{}`), nil
			}
		})
		if err := client.RemoveCredential(context.Background(), "oauth-ref"); err != nil {
			t.Fatalf("RemoveCredential() error = %v", err)
		}
		if err := client.RemoveCredential(context.Background(), "oauth-ref"); err != nil {
			t.Fatalf("missing RemoveCredential() error = %v", err)
		}
	})

	t.Run("API key", func(t *testing.T) {
		var putBody string
		client := managementTestClient(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == http.MethodGet && req.URL.Path == "/v0/management/auth-files":
				return managementJSONResponse(req, http.StatusOK, `{"files":[{"auth_index":"remove-ref","name":"runtime","provider":"claude","account_type":"api_key","status":"active"}]}`), nil
			case req.Method == http.MethodGet && req.URL.Path == "/v0/management/claude-api-key":
				return managementJSONResponse(req, http.StatusOK, `{"claude-api-key":[{"api-key":"remove-secret","base-url":"https://api.anthropic.com","auth-index":"remove-ref"},{"api-key":"keep-secret","base-url":"https://api.anthropic.com","auth-index":"keep-ref","unknown":{"keep":true}}]}`), nil
			case req.Method == http.MethodPut && req.URL.Path == "/v0/management/claude-api-key":
				body, _ := io.ReadAll(req.Body)
				putBody = string(body)
				return managementJSONResponse(req, http.StatusOK, `{"status":"ok"}`), nil
			default:
				return managementJSONResponse(req, http.StatusNotFound, `{}`), nil
			}
		})
		if err := client.RemoveCredential(context.Background(), "remove-ref"); err != nil {
			t.Fatalf("RemoveCredential() error = %v", err)
		}
		if strings.Contains(putBody, "remove-secret") || !strings.Contains(putBody, "keep-secret") || !strings.Contains(putBody, `"unknown":{"keep":true}`) {
			t.Fatalf("API-key removal body = %s", putBody)
		}
	})
}

func TestCredentialMutationRejectsAmbiguousReference(t *testing.T) {
	t.Parallel()

	var mutations atomic.Int32
	client := managementTestClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			mutations.Add(1)
		}
		return managementJSONResponse(req, http.StatusOK, `{"files":[{"auth_index":"duplicate","name":"a.json","provider":"codex"},{"auth_index":"duplicate","name":"b.json","provider":"claude"}]}`), nil
	})
	if err := client.SetCredentialDisabled(context.Background(), "duplicate", true); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("SetCredentialDisabled() error = %v", err)
	}
	if mutations.Load() != 0 {
		t.Fatalf("mutations = %d, want 0", mutations.Load())
	}
}

func managementTestClient(roundTrip roundTripFunc) *ManagementClient {
	return NewManagementClient(
		staticEndpointSource{endpoint: Endpoint{BaseURL: "http://127.0.0.1:12345", ManagementToken: "management-secret"}, ready: true},
		&http.Client{Transport: roundTrip},
	)
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
