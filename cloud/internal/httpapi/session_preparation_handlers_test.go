package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/go-chi/chi/v5"
)

type preparationHandlerStore struct {
	Store
	createdInput  domain.CreateSession
	commitInput   domain.CommitSessionPreparation
	commitSession string
	createCalls   int
	commitCalls   int
}

type concurrentPreparationStore struct {
	*preparationHandlerStore
	credentialStarted   chan struct{}
	orchestratorStarted chan struct{}
}

func (s *concurrentPreparationStore) ListProviderConnections(
	ctx context.Context,
	_ domain.Principal,
	_ string,
) ([]domain.ProviderConnection, error) {
	close(s.credentialStarted)
	select {
	case <-s.orchestratorStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []domain.ProviderConnection{{
		Provider: "codex", Label: defaultAgentConnectionLabel, ValidationState: "valid",
	}}, nil
}

func (s *concurrentPreparationStore) UpsertProviderConnection(
	context.Context,
	domain.Principal,
	string,
	string,
	string,
	[]byte,
	[]byte,
	json.RawMessage,
) (domain.ProviderConnection, error) {
	return domain.ProviderConnection{}, nil
}

func (s *concurrentPreparationStore) DeleteProviderConnection(
	context.Context,
	domain.Principal,
	string,
	string,
	string,
) error {
	return nil
}

func (s *concurrentPreparationStore) ProjectActiveOrchestrator(
	ctx context.Context,
	_, _ string,
) (string, string, bool, error) {
	close(s.orchestratorStarted)
	select {
	case <-s.credentialStarted:
	case <-ctx.Done():
		return "", "", false, ctx.Err()
	}
	return "", "", false, nil
}

func (s *preparationHandlerStore) CreateSession(
	_ context.Context,
	_ domain.Principal,
	_, _ string,
	_ int,
	input domain.CreateSession,
) (domain.Session, error) {
	s.createCalls++
	s.createdInput = input
	return domain.Session{ID: "00000000-0000-0000-0000-0000000000e5", Kind: input.Kind}, nil
}

func (s *preparationHandlerStore) CommitSessionPreparation(
	_ context.Context,
	_ domain.Principal,
	_, sessionID, _ string,
	input domain.CommitSessionPreparation,
) (domain.Session, error) {
	s.commitCalls++
	s.commitSession = sessionID
	s.commitInput = input
	return domain.Session{ID: sessionID, DisplayName: input.DisplayName, Kind: "worker"}, nil
}

func preparationRequest(t *testing.T, method, path, body, sessionID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "22222222-2222-2222-2222-222222222222")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", autolinkOrgID)
	if sessionID != "" {
		rctx.URLParams.Add("sessionId", sessionID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-0000000000f6"})
	return req.WithContext(ctx)
}

func TestPrepareSessionCreatesHiddenExpiringWorker(t *testing.T) {
	store := &preparationHandlerStore{}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	srv.prepareSession(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/session-preparations",
		`{"projectId":"`+autolinkProjectID+`","harness":"codex","provider":"nodeops"}`,
		"",
	))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
	if store.createCalls != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", store.createCalls)
	}
	if store.createdInput.Kind != "worker" || store.createdInput.Prompt != "" {
		t.Fatalf("prepared session = %+v", store.createdInput)
	}
	if store.createdInput.PreparationExpiresAfter != sessionPreparationTTL {
		t.Fatalf("preparation TTL = %v, want %v", store.createdInput.PreparationExpiresAfter, sessionPreparationTTL)
	}
}

func TestPrepareSessionRunsIndependentPreflightsConcurrently(t *testing.T) {
	store := &concurrentPreparationStore{
		preparationHandlerStore: &preparationHandlerStore{},
		credentialStarted:       make(chan struct{}),
		orchestratorStarted:     make(chan struct{}),
	}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	request := preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/session-preparations",
		`{"projectId":"`+autolinkProjectID+`","harness":"codex","provider":"nodeops"}`,
		"",
	)
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		srv.prepareSession(recorder, request.WithContext(ctx))
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("preflight checks did not overlap")
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCommitSessionPreparationQueuesFirstTask(t *testing.T) {
	store := &preparationHandlerStore{}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	sessionID := "00000000-0000-0000-0000-0000000000e5"

	srv.commitSessionPreparation(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/commit-preparation",
		`{"displayName":"Fix startup","prompt":"Run the checks"}`,
		sessionID,
	))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if store.commitCalls != 1 || store.commitSession != sessionID {
		t.Fatalf("commit = (%d, %q), want (1, %q)", store.commitCalls, store.commitSession, sessionID)
	}
	if store.commitInput.DisplayName != "Fix startup" || store.commitInput.Prompt != "Run the checks" {
		t.Fatalf("commit input = %+v", store.commitInput)
	}
}

func TestCommitSessionPreparationRejectsEmptyPrompt(t *testing.T) {
	store := &preparationHandlerStore{}
	srv := newChildServer(store, bothProviderProvisioning(sandbox.ProviderNodeOps), sandbox.ProviderNodeOps)
	recorder := httptest.NewRecorder()
	sessionID := "00000000-0000-0000-0000-0000000000e5"

	srv.commitSessionPreparation(recorder, preparationRequest(
		t,
		http.MethodPost,
		"/api/cloud/v1/orgs/"+autolinkOrgID+"/sessions/"+sessionID+"/commit-preparation",
		`{"displayName":"New task","prompt":"  "}`,
		sessionID,
	))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	if store.commitCalls != 0 {
		t.Fatalf("CommitSessionPreparation calls = %d, want 0", store.commitCalls)
	}
}
