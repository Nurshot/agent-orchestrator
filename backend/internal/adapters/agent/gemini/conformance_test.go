package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var requiredHelpTokens = []string{
	"--prompt-interactive", "--approval-mode", "--model",
	"--session-id", "--resume", "--acp", "--skip-trust",
}

type releaseContract struct {
	Version      string `json:"version"`
	BinarySHA256 string `json:"binarySHA256"`
	Platform     string `json:"platform"`
	PromptExport bool   `json:"promptExport"`
	SessionID    bool   `json:"sessionID"`
	Restore      bool   `json:"restore"`
	Hooks        bool   `json:"hooks"`
	ACP          bool   `json:"acp"`
}

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func TestParseVersionAtLeast060(t *testing.T) {
	tests := []struct {
		output string
		want   bool
	}{
		{"0.60.0", true},
		{"gemini-cli 1.0.0", true},
		{"0.59.9", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			_, got := supportedVersion(tt.output)
			if got != tt.want {
				t.Fatalf("supportedVersion(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestLiveGeminiReleaseContract(t *testing.T) {
	if os.Getenv("AO_LIVE_GEMINI") != "1" {
		t.Skip("set AO_LIVE_GEMINI=1 to test the installed Gemini CLI")
	}

	binary, err := exec.LookPath("gemini")
	if err != nil {
		t.Fatalf("resolve Gemini CLI: %v", err)
	}
	versionOutput, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("gemini --version: %v: %s", err, versionOutput)
	}
	version, ok := supportedVersion(string(versionOutput))
	if !ok {
		t.Fatalf("Gemini CLI version %q is below required 0.60.0 or unparsable", strings.TrimSpace(string(versionOutput)))
	}
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("gemini --help: %v: %s", err, help)
	}
	for _, token := range requiredHelpTokens {
		if !strings.Contains(string(help), token) {
			t.Errorf("gemini --help missing required token %q", token)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	bytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read Gemini executable: %v", err)
	}
	sum := sha256.Sum256(bytes)
	contract := releaseContract{
		Version: version, BinarySHA256: hex.EncodeToString(sum[:]), Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	fixture, err := os.ReadFile("testdata/contract-v0.60.0.json")
	if err != nil {
		t.Fatalf("read contract fixture: %v", err)
	}
	var proven releaseContract
	if err := json.Unmarshal(fixture, &proven); err != nil {
		t.Fatalf("decode contract fixture: %v", err)
	}
	if !proven.PromptExport || !proven.SessionID || !proven.Restore || !proven.Hooks || !proven.ACP {
		t.Fatalf("release contract is not behaviorally proven for %s; run and record prompt export, session ID, restore, hooks, and ACP probes before registration", contract.Platform)
	}
}

func supportedVersion(output string) (string, bool) {
	match := versionPattern.FindStringSubmatch(output)
	if match == nil {
		return "", false
	}
	parts := [3]int{}
	for i := range parts {
		parts[i], _ = strconv.Atoi(match[i+1])
	}
	return match[0], parts[0] > 0 || parts[1] >= 60
}
