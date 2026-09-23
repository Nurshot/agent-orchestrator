package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/githubapp"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// ghRoutingStubStore satisfies githubapp.Store; only the methods the two callback
// paths reach are implemented. Every other method is promoted from the embedded
// nil interface and panics if called, which keeps the test honest about the call
// graph each path exercises.
type ghRoutingStubStore struct {
	githubapp.Store
	beginCalls int
}

func (s *ghRoutingStubStore) ValidateGitHubInstallState(context.Context, []byte) error { return nil }

func (s *ghRoutingStubStore) BeginGitHubOAuth(
	context.Context, []byte, domain.GitHubInstallation, []byte, []byte, []byte, time.Time,
) (domain.GitHubInstallAttempt, error) {
	s.beginCalls++
	return domain.GitHubInstallAttempt{}, nil
}

func (s *ghRoutingStubStore) GitHubOAuthAttempt(context.Context, []byte) (domain.GitHubInstallAttempt, error) {
	return domain.GitHubInstallAttempt{}, postgres.ErrNotFound
}

func newGitHubCallbackTestServer(t *testing.T) (*Server, *ghRoutingStubStore) {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/app/installations/123" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                   123,
				"repository_selection": "all",
				// A user account supports the authority proof without needing any
				// installation permissions, keeping the fixture minimal.
				"account": map[string]any{"id": 1, "login": "octocat", "type": "User"},
			})
			return
		}
		t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(gh.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	client, err := githubapp.New(githubapp.Config{
		AppID:         1,
		AppSlug:       "ao-test",
		ClientID:      "Iv1.test",
		ClientSecret:  "secret",
		PrivateKeyPEM: string(pemBytes),
		PublicURL:     "https://api.example.com",
		APIBaseURL:    gh.URL,
		WebBaseURL:    gh.URL,
	}, gh.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	store := &ghRoutingStubStore{}
	svc, err := githubapp.NewService(
		store, client,
		make([]byte, 32), make([]byte, 32),
		"webhook-secret", time.Hour, slog.Default(),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return &Server{github: svc, logger: slog.Default()}, store
}

// A GitHub App that requests user authorization during installation delivers the
// installation context (installation_id) straight to the OAuth callback in one
// redirect. The handler must recognize that shape and run the setup step,
// bouncing to the authorize page, rather than trying to complete an OAuth attempt
// that was never started.
func TestGitHubOAuthCallbackBundledInstallRedirectsToAuthorize(t *testing.T) {
	srv, store := newGitHubCallbackTestServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/cloud/v1/github/oauth/callback?installation_id=123&setup_action=update&state=teststate&code=abc", nil)
	rec := httptest.NewRecorder()

	srv.githubOAuthCallback(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to authorize)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/login/oauth/authorize") {
		t.Fatalf("Location = %q, want the GitHub authorize URL", loc)
	}
	if store.beginCalls != 1 {
		t.Fatalf("BeginGitHubOAuth called %d times, want 1", store.beginCalls)
	}
}

// A normal completion callback (code+state, no installation_id) must go through
// CompleteOAuth, never the setup redirect. The stub has no OAuth attempt, so
// completion fails with the 400 error page, which still distinguishes the path.
func TestGitHubOAuthCallbackCompletionPathDoesNotRedirect(t *testing.T) {
	srv, store := newGitHubCallbackTestServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/cloud/v1/github/oauth/callback?state=teststate&code=abc", nil)
	rec := httptest.NewRecorder()

	srv.githubOAuthCallback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (CompleteOAuth path with no attempt)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("unexpected redirect to %q; the completion path must not redirect", loc)
	}
	if store.beginCalls != 0 {
		t.Fatalf("BeginGitHubOAuth called %d times, want 0 on the completion path", store.beginCalls)
	}
}
