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

func TestAddAPIKeyPreservesUnknownFieldsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	const newKey = "new-private-key"
	var puts atomic.Int32
	var added atomic.Bool
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v0/management/codex-api-key" {
			return managementJSONResponse(req, http.StatusNotFound, `{}`), nil
		}
		switch req.Method {
		case http.MethodGet:
			items := `[{"api-key":"existing-key","base-url":"https://api.openai.com/v1","custom":{"keep":true},"auth-index":"existing-ref"}`
			if added.Load() {
				items += `,{"api-key":"` + newKey + `","base-url":"https://api.openai.com/v1","auth-index":"new-ref"}`
			}
			return managementJSONResponse(req, http.StatusOK, `{"codex-api-key":`+items+`]}`), nil
		case http.MethodPut:
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), `"custom":{"keep":true}`) || !strings.Contains(string(body), `"api-key":"`+newKey+`"`) {
				t.Fatalf("PUT did not preserve existing raw fields: %s", body)
			}
			puts.Add(1)
			added.Store(true)
			return managementJSONResponse(req, http.StatusOK, `{"status":"ok"}`), nil
		default:
			return managementJSONResponse(req, http.StatusMethodNotAllowed, `{}`), nil
		}
	})
	client := NewManagementClient(
		staticEndpointSource{endpoint: Endpoint{BaseURL: "http://127.0.0.1:12345", ManagementToken: "management-secret"}, ready: true},
		&http.Client{Transport: transport},
	)

	input := APIKeyInput{Provider: ProviderCodex, Key: newKey}
	first, err := client.AddAPIKey(context.Background(), input)
	if err != nil {
		t.Fatalf("AddAPIKey() error = %v", err)
	}
	second, err := client.AddAPIKey(context.Background(), input)
	if err != nil {
		t.Fatalf("duplicate AddAPIKey() error = %v", err)
	}
	if first.Ref != "new-ref" || second.Ref != first.Ref || first.Provider != ProviderCodex || first.Kind != "api_key" {
		t.Fatalf("AddAPIKey() summaries = %#v %#v", first, second)
	}
	if puts.Load() != 1 {
		t.Fatalf("PUT calls = %d, want 1", puts.Load())
	}
}

func TestAddAPIKeyValidatesProviderKeyAndBaseURL(t *testing.T) {
	t.Parallel()

	client := NewManagementClient(nil, nil)
	for _, tt := range []struct {
		name  string
		input APIKeyInput
		want  error
	}{
		{name: "unsupported provider", input: APIKeyInput{Provider: Provider("gemini"), Key: "key"}, want: ErrUnsupportedProvider},
		{name: "empty key", input: APIKeyInput{Provider: ProviderCodex}, want: ErrInvalidCredential},
		{name: "oversized key", input: APIKeyInput{Provider: ProviderCodex, Key: strings.Repeat("x", managementAPIKeyLimit+1)}, want: ErrRequestTooLarge},
		{name: "remote HTTP", input: APIKeyInput{Provider: ProviderClaude, Key: "key", BaseURL: "http://example.com"}, want: ErrInvalidCredential},
		{name: "userinfo", input: APIKeyInput{Provider: ProviderClaude, Key: "key", BaseURL: "https://user@example.com"}, want: ErrInvalidCredential},
		{name: "query", input: APIKeyInput{Provider: ProviderClaude, Key: "key", BaseURL: "https://example.com?q=1"}, want: ErrInvalidCredential},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.AddAPIKey(context.Background(), tt.input); !errors.Is(err, tt.want) {
				t.Fatalf("AddAPIKey() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestImportCredentialValidatesUploadsAndVerifiesCatalog(t *testing.T) {
	t.Parallel()

	var uploaded atomic.Bool
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v0/management/auth-files":
			files := `[]`
			if uploaded.Load() {
				files = `[{"auth_index":"import-ref","name":"safe-claude.json","provider":"claude","type":"claude","account_type":"oauth","status":"active"}]`
			}
			return managementJSONResponse(req, http.StatusOK, `{"files":`+files+`}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v0/management/auth-files":
			if req.URL.Query().Get("name") != "safe-claude.json" {
				t.Fatalf("upload name = %q", req.URL.Query().Get("name"))
			}
			body, _ := io.ReadAll(req.Body)
			var object map[string]any
			if json.Unmarshal(body, &object) != nil || object["type"] != "claude" {
				t.Fatalf("upload body = %s", body)
			}
			uploaded.Store(true)
			return managementJSONResponse(req, http.StatusOK, `{"status":"ok"}`), nil
		default:
			return managementJSONResponse(req, http.StatusNotFound, `{}`), nil
		}
	})
	client := NewManagementClient(
		staticEndpointSource{endpoint: Endpoint{BaseURL: "http://127.0.0.1:12345", ManagementToken: "management-secret"}, ready: true},
		&http.Client{Transport: transport},
	)
	got, err := client.ImportCredential(context.Background(), CredentialImport{
		Provider: ProviderClaude,
		Name:     "safe-claude.json",
		JSON:     json.RawMessage(`{"type":"claude","access_token":"private"}`),
	})
	if err != nil {
		t.Fatalf("ImportCredential() error = %v", err)
	}
	if got.Ref != "import-ref" || got.Provider != ProviderClaude || got.Kind != "oauth" {
		t.Fatalf("ImportCredential() = %#v", got)
	}
}

func TestImportCredentialRejectsUnsafeInputsBeforeRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := NewManagementClient(
		staticEndpointSource{endpoint: Endpoint{BaseURL: "http://127.0.0.1:12345", ManagementToken: "management-secret"}, ready: true},
		&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return managementJSONResponse(req, http.StatusOK, `{}`), nil
		})},
	)
	inputs := []CredentialImport{
		{Provider: Provider("gemini"), Name: "safe.json", JSON: json.RawMessage(`{"type":"gemini"}`)},
		{Provider: ProviderCodex, Name: "../unsafe.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		{Provider: ProviderCodex, Name: ".oauth-codex-secret.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		{Provider: ProviderCodex, Name: "quota_probe.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		{Provider: ProviderCodex, Name: "safe.txt", JSON: json.RawMessage(`{"type":"codex"}`)},
		{Provider: ProviderCodex, Name: "safe.json", JSON: json.RawMessage(`{"type":"claude"}`)},
		{Provider: ProviderCodex, Name: "safe.json", JSON: json.RawMessage(`[]`)},
	}
	for _, input := range inputs {
		if _, err := client.ImportCredential(context.Background(), input); err == nil {
			t.Fatalf("ImportCredential(%q) unexpectedly succeeded", input.Name)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("requests = %d, want 0", calls.Load())
	}
}
