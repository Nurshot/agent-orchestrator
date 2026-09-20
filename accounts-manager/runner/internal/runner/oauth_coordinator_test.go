package runner

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runnerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f runnerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type inertListener struct {
	closed chan struct{}
	once   sync.Once
	addr   net.Addr
}

func newInertListener(address string) *inertListener {
	return &inertListener{closed: make(chan struct{}), addr: runnerTestAddr(address)}
}

func (l *inertListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *inertListener) Close() error              { l.once.Do(func() { close(l.closed) }); return nil }
func (l *inertListener) Addr() net.Addr            { return l.addr }

type runnerTestAddr string

func (a runnerTestAddr) Network() string { return "tcp" }
func (a runnerTestAddr) String() string  { return string(a) }

func TestOAuthCoordinatorRequiresManagementKey(t *testing.T) {
	t.Parallel()

	coordinator := newOAuthCoordinator("http://127.0.0.1:12345", "management-key", &http.Client{}, nil)
	defer coordinator.Close()
	for _, token := range []string{"", "control-key", "data-plane-key"} {
		for _, request := range []*http.Request{
			httptest.NewRequest(http.MethodPost, "/ao/internal/oauth/start", strings.NewReader(`{"provider":"codex"}`)),
			httptest.NewRequest(http.MethodGet, "/ao/internal/oauth/status?state=opaque", nil),
			httptest.NewRequest(http.MethodDelete, "/ao/internal/oauth/session?state=opaque", nil),
		} {
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			response := httptest.NewRecorder()
			coordinator.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("token=%q %s %s status = %d, want 401", token, request.Method, request.URL.Path, response.Code)
			}
		}
	}
}

func TestOAuthCoordinatorBindsLoopbackBeforeRequestingAuthorizationURL(t *testing.T) {
	t.Parallel()

	var upstreamCalls atomic.Int32
	client := &http.Client{Transport: runnerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		upstreamCalls.Add(1)
		return jsonHTTPResponse(req, http.StatusOK, `{"status":"ok","url":"https://auth.example.test/start","state":"opaque-state"}`), nil
	})}
	var gotNetwork, gotAddress string
	listenErr := errors.New("port busy")
	coordinator := newOAuthCoordinator("http://127.0.0.1:12345", "management-key", client, func(network, address string) (net.Listener, error) {
		gotNetwork, gotAddress = network, address
		return nil, listenErr
	})
	defer coordinator.Close()

	request := httptest.NewRequest(http.MethodPost, "/ao/internal/oauth/start", strings.NewReader(`{"provider":"codex"}`))
	request.Header.Set("Authorization", "Bearer management-key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	coordinator.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
	if gotNetwork != "tcp" || gotAddress != "127.0.0.1:1455" {
		t.Fatalf("listen = %s %s, want tcp 127.0.0.1:1455", gotNetwork, gotAddress)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0 before listener is ready", upstreamCalls.Load())
	}
	if strings.Contains(response.Body.String(), listenErr.Error()) {
		t.Fatal("response exposed listener error")
	}
}

func TestOAuthCoordinatorReusesPendingProviderAndAllowsOtherProvider(t *testing.T) {
	t.Parallel()

	var upstreamCalls atomic.Int32
	client := &http.Client{Transport: runnerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		call := upstreamCalls.Add(1)
		provider := "codex"
		if strings.Contains(req.URL.Path, "anthropic") {
			provider = "claude"
		}
		return jsonHTTPResponse(req, http.StatusOK, `{"status":"ok","url":"https://auth.example.test/`+provider+`","state":"state-`+provider+`-`+string(rune('0'+call))+`"}`), nil
	})}
	var listenersMu sync.Mutex
	listeners := make([]*inertListener, 0, 2)
	coordinator := newOAuthCoordinator("http://127.0.0.1:12345", "management-key", client, func(_, address string) (net.Listener, error) {
		listener := newInertListener(address)
		listenersMu.Lock()
		listeners = append(listeners, listener)
		listenersMu.Unlock()
		return listener, nil
	})
	defer coordinator.Close()

	start := func(provider string) string {
		request := httptest.NewRequest(http.MethodPost, "/ao/internal/oauth/start", strings.NewReader(`{"provider":"`+provider+`"}`))
		request.Header.Set("Authorization", "Bearer management-key")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		coordinator.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("start %s status = %d body=%s", provider, response.Code, response.Body.String())
		}
		return response.Body.String()
	}

	firstCodex := start("codex")
	secondCodex := start("codex")
	if firstCodex != secondCodex {
		t.Fatalf("same-provider retry returned different session:\n%s\n%s", firstCodex, secondCodex)
	}
	_ = start("claude")
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream start calls = %d, want 2", upstreamCalls.Load())
	}
	listenersMu.Lock()
	listenerCount := len(listeners)
	listenersMu.Unlock()
	if listenerCount != 2 {
		t.Fatalf("listeners = %d, want 2", listenerCount)
	}
}

func TestOAuthCallbackValidatesStateAndForwardsWithManagementAuthentication(t *testing.T) {
	t.Parallel()

	var forwarded atomic.Int32
	client := &http.Client{Transport: runnerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v0/management/oauth-callback" || req.Method != http.MethodPost {
			t.Fatalf("unexpected upstream request: %s %s", req.Method, req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer management-key" {
			t.Fatal("callback was not forwarded with management authentication")
		}
		body, _ := io.ReadAll(req.Body)
		if !bytes.Contains(body, []byte(`"state":"opaque-state"`)) || !bytes.Contains(body, []byte(`"code":"private-code"`)) {
			t.Fatalf("callback body missing expected values: %s", body)
		}
		forwarded.Add(1)
		return jsonHTTPResponse(req, http.StatusOK, `{"status":"ok"}`), nil
	})}
	coordinator := newOAuthCoordinator("http://127.0.0.1:12345", "management-key", client, nil)
	defer coordinator.Close()
	session := &oauthRunnerSession{provider: "codex", state: "opaque-state", status: "pending", expiresAt: time.Now().Add(time.Minute)}
	coordinator.sessions[session.state] = session
	coordinator.providers[session.provider] = session.state

	wrong := httptest.NewRecorder()
	coordinator.callbackHandler(session).ServeHTTP(wrong, httptest.NewRequest(http.MethodGet, "/auth/callback?state=wrong&code=private-code", nil))
	if wrong.Code != http.StatusBadRequest || forwarded.Load() != 0 {
		t.Fatalf("wrong-state callback status=%d forwarded=%d", wrong.Code, forwarded.Load())
	}

	valid := httptest.NewRecorder()
	coordinator.callbackHandler(session).ServeHTTP(valid, httptest.NewRequest(http.MethodGet, "/auth/callback?state=opaque-state&code=private-code", nil))
	if valid.Code != http.StatusOK || forwarded.Load() != 1 {
		t.Fatalf("valid callback status=%d forwarded=%d", valid.Code, forwarded.Load())
	}
	for _, secret := range []string{"opaque-state", "private-code"} {
		if strings.Contains(valid.Body.String(), secret) {
			t.Fatalf("browser response exposed %q", secret)
		}
	}

	coordinator.mu.Lock()
	delete(coordinator.sessions, session.state)
	coordinator.mu.Unlock()
	late := httptest.NewRecorder()
	coordinator.callbackHandler(session).ServeHTTP(late, httptest.NewRequest(http.MethodGet, "/auth/callback?state=opaque-state&code=late-code", nil))
	if late.Code != http.StatusGone || forwarded.Load() != 1 {
		t.Fatalf("late callback status=%d forwarded=%d", late.Code, forwarded.Load())
	}
}

func TestOAuthCoordinatorStatusAndCancelAreSafeAndIdempotent(t *testing.T) {
	t.Parallel()

	var cancels atomic.Int32
	client := &http.Client{Transport: runnerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v0/management/get-auth-status":
			return jsonHTTPResponse(req, http.StatusOK, `{"status":"wait"}`), nil
		case req.Method == http.MethodDelete && req.URL.Path == "/v0/management/oauth-session":
			cancels.Add(1)
			return jsonHTTPResponse(req, http.StatusOK, `{"status":"ok","cancelled":true}`), nil
		default:
			return nil, errors.New("unexpected request")
		}
	})}
	coordinator := newOAuthCoordinator("http://127.0.0.1:12345", "management-key", client, nil)
	defer coordinator.Close()
	coordinator.sessions["opaque-state"] = &oauthRunnerSession{provider: "codex", state: "opaque-state", status: "pending", expiresAt: time.Now().Add(time.Minute)}
	coordinator.providers["codex"] = "opaque-state"

	statusRequest := httptest.NewRequest(http.MethodGet, "/ao/internal/oauth/status?state=opaque-state", nil)
	statusRequest.Header.Set("Authorization", "Bearer management-key")
	statusResponse := httptest.NewRecorder()
	coordinator.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"status":"pending"`) {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}

	for range 2 {
		cancelRequest := httptest.NewRequest(http.MethodDelete, "/ao/internal/oauth/session?state=opaque-state", nil)
		cancelRequest.Header.Set("Authorization", "Bearer management-key")
		cancelResponse := httptest.NewRecorder()
		coordinator.ServeHTTP(cancelResponse, cancelRequest)
		if cancelResponse.Code != http.StatusNoContent {
			t.Fatalf("cancel status = %d, want 204", cancelResponse.Code)
		}
	}
	if cancels.Load() != 1 {
		t.Fatalf("upstream cancel calls = %d, want 1", cancels.Load())
	}
}

func jsonHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
