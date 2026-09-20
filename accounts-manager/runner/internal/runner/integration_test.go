package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const runnerHelperEnvironment = "AO_ACCOUNTS_MANAGER_RUNNER_HELPER"

func TestRunnerHelperProcess(t *testing.T) {
	if os.Getenv(runnerHelperEnvironment) != "1" {
		return
	}
	code := RunCLI(context.Background(), []string{"serve", "--state-dir", os.Getenv("AO_ACCOUNTS_MANAGER_TEST_STATE")}, os.Stdout, os.Stderr)
	os.Exit(code)
}

func TestRunnerStreamsThroughFakeOpenAIProviderWithoutLeakingSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration in short mode")
	}

	const (
		controlKey    = "control-secret-do-not-log"
		clientKey     = "client-secret-do-not-log"
		managementKey = "management-secret-do-not-log"
		providerKey   = "provider-secret-do-not-log"
		requestSecret = "request-body-secret-do-not-log"
	)
	upstreamRequest := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+providerKey {
			http.Error(w, "unexpected provider credential", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		upstreamRequest <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello \"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	runnerPort := reservePort(t)
	stateDir := t.TempDir()
	chmodPrivateDir(t, stateDir)
	authDir := filepath.Join(stateDir, "auth")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(stateDir, "control.key"), controlKey+"\n")
	writePrivateFile(t, filepath.Join(stateDir, "management.key"), managementKey+"\n")
	config := fmt.Sprintf(`host: 127.0.0.1
port: %d
auth-dir: %s
api-keys:
  - %s
request-log: false
logging-to-file: false
usage-statistics-enabled: false
remote-management:
  allow-remote: false
  secret-key: ''
  disable-control-panel: true
  disable-auto-update-panel: true
plugins:
  enabled: false
pprof:
  enable: false
discovery:
  enabled: false
openai-compatibility:
  - name: fake-provider
    base-url: %s/v1
    api-key-entries:
      - api-key: %s
    models:
      - name: fake-model
        alias: fake-model
`, runnerPort, authDir, clientKey, upstream.URL, providerKey)
	writePrivateFile(t, filepath.Join(stateDir, "config.yaml"), config)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	command := exec.Command(executable, "-test.run=^TestRunnerHelperProcess$")
	command.Env = append(os.Environ(), runnerHelperEnvironment+"=1", "AO_ACCOUNTS_MANAGER_TEST_STATE="+stateDir, "GIN_MODE=release")
	command.Stdout = logs
	command.Stderr = logs
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", runnerPort)
	waitForRunnerHealth(t, baseURL)

	runtimeBytes, err := os.ReadFile(filepath.Join(stateDir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runtimeRecord RuntimeRecord
	if err = json.Unmarshal(runtimeBytes, &runtimeRecord); err != nil {
		t.Fatal(err)
	}
	identityRequest, _ := http.NewRequest(http.MethodGet, baseURL+"/ao/internal/identity", nil)
	identityRequest.Header.Set("Authorization", "Bearer "+controlKey)
	identityResponse, err := http.DefaultClient.Do(identityRequest)
	if err != nil {
		t.Fatal(err)
	}
	identityBody, _ := io.ReadAll(identityResponse.Body)
	_ = identityResponse.Body.Close()
	if identityResponse.StatusCode != http.StatusOK || !bytes.Contains(identityBody, []byte(runtimeRecord.InstanceID)) {
		t.Fatalf("identity status=%d body=%s", identityResponse.StatusCode, identityBody)
	}

	requestBody := fmt.Sprintf(`{"model":"fake-model","messages":[{"role":"user","content":"%s"}],"stream":true}`, requestSecret)
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+clientKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s logs=%s", response.StatusCode, stream, logs.String())
	}
	for _, chunk := range []string{"hello", "world", "[DONE]"} {
		if !bytes.Contains(stream, []byte(chunk)) {
			t.Fatalf("stream missing %q: %s", chunk, stream)
		}
	}

	select {
	case got := <-upstreamRequest:
		if !bytes.Contains(got, []byte(requestSecret)) {
			t.Fatalf("upstream request did not receive request content: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("fake provider did not receive the request")
	}

	combinedPublicOutput := strings.Join([]string{string(runtimeBytes), string(identityBody), logs.String()}, "\n")
	for _, secret := range []string{controlKey, clientKey, managementKey, providerKey, requestSecret} {
		if strings.Contains(combinedPublicOutput, secret) {
			t.Fatalf("runner exposed secret %q", secret)
		}
	}
}

func TestServeRequiresManagementKeyForManagementAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner integration in short mode")
	}

	const (
		controlKey    = "management-test-control"
		clientKey     = "management-test-client"
		managementKey = "management-test-secret"
	)
	runnerPort := reservePort(t)
	stateDir := t.TempDir()
	chmodPrivateDir(t, stateDir)
	authDir := filepath.Join(stateDir, "auth")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(stateDir, "control.key"), controlKey+"\n")
	writePrivateFile(t, filepath.Join(stateDir, "management.key"), managementKey+"\n")
	config := fmt.Sprintf(`host: 127.0.0.1
port: %d
auth-dir: %s
api-keys:
  - %s
request-log: false
logging-to-file: false
usage-statistics-enabled: false
remote-management:
  allow-remote: false
  secret-key: ''
  disable-control-panel: true
  disable-auto-update-panel: true
plugins:
  enabled: false
pprof:
  enable: false
discovery:
  enabled: false
routing:
  strategy: round-robin
`, runnerPort, authDir, clientKey)
	writePrivateFile(t, filepath.Join(stateDir, "config.yaml"), config)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	command := exec.Command(executable, "-test.run=^TestRunnerHelperProcess$")
	command.Env = append(os.Environ(), runnerHelperEnvironment+"=1", "AO_ACCOUNTS_MANAGER_TEST_STATE="+stateDir, "GIN_MODE=release")
	command.Stdout = logs
	command.Stderr = logs
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", runnerPort)
	waitForRunnerHealth(t, baseURL)
	managementURL := baseURL + "/v0/management/routing/strategy"

	for _, tt := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "no token", want: http.StatusUnauthorized},
		{name: "data plane key", token: clientKey, want: http.StatusUnauthorized},
		{name: "management key", token: managementKey, want: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request, requestErr := http.NewRequest(http.MethodGet, managementURL, nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			if tt.token != "" {
				request.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			defer response.Body.Close()
			if response.StatusCode != tt.want {
				t.Fatalf("management status = %d, want %d", response.StatusCode, tt.want)
			}
			if tt.want == http.StatusOK {
				var payload struct {
					Strategy string `json:"strategy"`
				}
				if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Strategy != "round-robin" && payload.Strategy != "weighted-round-robin" && payload.Strategy != "fill-first" {
					t.Fatalf("unexpected routing strategy %q", payload.Strategy)
				}
			}
		})
	}

	response, err := http.Get(baseURL + "/management.html")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("management panel status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
	if logsText := logs.String(); strings.Contains(logsText, controlKey) || strings.Contains(logsText, clientKey) || strings.Contains(logsText, managementKey) {
		t.Fatal("runner logs exposed a private key")
	}
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func waitForRunnerHealth(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("runner did not become healthy")
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
