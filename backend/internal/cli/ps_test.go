package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPSJSONOutputUsesPublicContractFieldNames(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/system/processes" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"processes":[{"pid":42,"rssBytes":1048576,"group":"sessions","sessionId":"worker-7"}],"totalRssBytes":1048576}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "ps", "--json")
	if err != nil {
		t.Fatalf("ao ps --json: %v\nstderr=%s", err, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	rows, ok := got["processes"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("processes = %#v", got["processes"])
	}
	row := rows[0].(map[string]any)
	for _, key := range []string{"pid", "rssBytes", "group", "sessionId"} {
		if _, ok := row[key]; !ok {
			t.Errorf("missing lower-camel key %q in %#v", key, row)
		}
	}
	for _, key := range []string{"PID", "RSSBytes", "Group", "SessionID"} {
		if _, ok := row[key]; ok {
			t.Errorf("unexpected Go field key %q in %#v", key, row)
		}
	}
}
