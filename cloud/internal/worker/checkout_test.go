package worker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type staleBranchRunner struct {
	cloneURL    string
	cloneBranch []string
}

func (r *staleBranchRunner) Run(_ context.Context, directory string, _ map[string]string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "clone" {
		for i, arg := range args {
			if arg == "--branch" && i+1 < len(args) {
				r.cloneBranch = append(r.cloneBranch, args[i+1])
				if args[i+1] == "main" {
					if err := os.WriteFile(
						filepath.Join(filepath.Dir(directory), ".harness-ready"),
						[]byte("ready"),
						0o644,
					); err != nil {
						return "", err
					}
					return "", errors.New("remote branch not found")
				}
			}
		}
		destination := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(destination, ".git"), 0o755); err != nil {
			return "", err
		}
		return "", nil
	}
	if len(args) > 0 && args[0] == "ls-remote" {
		return "ref: refs/heads/master\tHEAD\n123456\tHEAD\n", nil
	}
	return r.cloneURL, nil
}

// stagingRecorderRunner fakes git for the non-empty-workspace clone path. It
// records the working directory of the clone (the staging root) and populates
// the clone destination with a .git dir and a tracked file so the merge and
// origin validation succeed.
type stagingRecorderRunner struct {
	cloneURL   string
	stagingDir string
	cloneArgs  []string
}

func (r *stagingRecorderRunner) Run(_ context.Context, dir string, _ map[string]string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "clone" {
		r.stagingDir = dir
		r.cloneArgs = append([]string(nil), args...)
		dest := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("# repo\n"), 0o644); err != nil {
			return "", err
		}
		return "", nil
	}
	// validateOrigin: `remote get-url origin`
	return r.cloneURL, nil
}

// A workspace the coding agent has already written into (non-empty, no .git)
// must check out even when its parent is the provider's durable root that the
// worker user cannot write to. The fix stages inside the workspace itself, so
// the staging dir must live under the workspace, never under its parent.
func TestCloneIntoNonEmptyWorkspaceStagesInsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "repository")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent wrote a file before checkout ran — this is what forces the
	// non-empty (staging) path.
	if err := os.WriteFile(filepath.Join(workspace, ".claude"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &stagingRecorderRunner{cloneURL: "https://github.com/acme/repo.git"}
	if err := PrepareCheckout(context.Background(), runner, workspace,
		CheckoutGrantResponse{CloneURL: "https://github.com/acme/repo.git"}, "main"); err != nil {
		t.Fatalf("PrepareCheckout: %v", err)
	}

	if r := runner.stagingDir; !strings.HasPrefix(filepath.Clean(r), filepath.Clean(workspace)+string(filepath.Separator)) {
		t.Fatalf("staging dir %q is not inside the workspace %q (would fail on a durable root the worker cannot write)", r, workspace)
	}
	cloneCommand := strings.Join(runner.cloneArgs, " ")
	for _, required := range []string{"--no-tags", "--single-branch", "--filter=blob:none", "--branch main"} {
		if !strings.Contains(cloneCommand, required) {
			t.Fatalf("clone command %q is missing %q", cloneCommand, required)
		}
	}
	// The clone's tracked files were merged in and the agent's file preserved.
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("cloned .git not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatalf("cloned file not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".claude")); err != nil {
		t.Fatalf("agent file not preserved: %v", err)
	}
	// The hidden staging dir is cleaned up before returning.
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ao-checkout-") {
			t.Fatalf("staging dir %q was left behind in the workspace", e.Name())
		}
	}
}

func TestPrepareCheckoutRequiresDefaultBranch(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "repository")
	runner := &stagingRecorderRunner{cloneURL: "https://github.com/acme/repo.git"}
	err := PrepareCheckout(
		context.Background(),
		runner,
		workspace,
		CheckoutGrantResponse{CloneURL: "https://github.com/acme/repo.git"},
		" ",
	)
	if err == nil || !strings.Contains(err.Error(), "default branch is required") {
		t.Fatalf("PrepareCheckout error = %v, want default-branch validation", err)
	}
}

func TestPrepareCheckoutRecoversFromStaleDefaultBranch(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "repository")
	runner := &staleBranchRunner{cloneURL: "https://github.com/acme/repo.git"}
	if err := PrepareCheckout(
		context.Background(),
		runner,
		workspace,
		CheckoutGrantResponse{CloneURL: runner.cloneURL},
		"main",
	); err != nil {
		t.Fatalf("PrepareCheckout: %v", err)
	}
	if got := strings.Join(runner.cloneBranch, ","); got != "main,master" {
		t.Fatalf("clone branches = %q, want main,master", got)
	}
	if content, err := os.ReadFile(filepath.Join(workspace, ".harness-ready")); err != nil || string(content) != "ready" {
		t.Fatalf("concurrent harness file = %q, %v", content, err)
	}
}

func TestConfigureWorkerGitRepairsUnbornHead(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	source := filepath.Join(root, "source")
	workspace := filepath.Join(root, "workspace")
	dataDir := filepath.Join(root, "data")

	runCheckoutGit(t, root, "init", "--bare", origin)
	runCheckoutGit(t, root, "init", "--initial-branch", "main", source)
	runCheckoutGit(t, source, "config", "user.name", "Checkout Test")
	runCheckoutGit(t, source, "config", "user.email", "checkout-test@example.com")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCheckoutGit(t, source, "add", "README.md")
	runCheckoutGit(t, source, "commit", "-m", "initial")
	runCheckoutGit(t, source, "remote", "add", "origin", origin)
	runCheckoutGit(t, source, "push", "origin", "main")
	runCheckoutGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	runCheckoutGit(t, root, "clone", "--no-checkout", origin, workspace)

	if err := os.WriteFile(
		filepath.Join(workspace, ".git", "HEAD"),
		[]byte("ref: refs/heads/.invalid\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := ConfigureWorkerGit(
		ctx,
		ExecGitRunner{},
		workspace,
		dataDir,
		"https://cloud.example.com",
		"session-1",
		"ao/session-1",
		"main",
	); err != nil {
		t.Fatalf("ConfigureWorkerGit: %v", err)
	}

	if got := strings.TrimSpace(runCheckoutGit(t, workspace, "symbolic-ref", "--short", "HEAD")); got != "ao/session-1" {
		t.Fatalf("HEAD branch = %q, want %q", got, "ao/session-1")
	}
	if got := strings.TrimSpace(runCheckoutGit(t, workspace, "show", "HEAD:README.md")); got != "ready" {
		t.Fatalf("README at repaired HEAD = %q, want %q", got, "ready")
	}
}

func runCheckoutGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
