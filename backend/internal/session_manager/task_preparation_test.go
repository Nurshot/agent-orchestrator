package sessionmanager

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestTaskPreparationIsClaimedWithoutCreatingAnotherWorktree(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }
	project := st.projects["mer"]

	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	prepared := st.sessions["mer-1"]
	if !prepared.IsTaskPreparation || prepared.Metadata.WorkspacePath == "" {
		t.Fatalf("prepared row = %+v, want hidden row with workspace", prepared)
	}

	spawned, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID:       "mer",
		Kind:            domain.KindWorker,
		TaskPreparation: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spawned.ID != "mer-1" {
		t.Fatalf("session id = %q, want reserved mer-1", spawned.ID)
	}
	if got := ws.createCount; got != 1 {
		t.Fatalf("workspace creates = %d, want speculative create only", got)
	}
	if st.sessions[spawned.ID].IsTaskPreparation {
		t.Fatal("claimed session remained hidden")
	}
	if got := st.sessions[spawned.ID].ProvisionState.WithDefault(); got != domain.SessionProvisionReady {
		t.Fatalf("synchronous task provision state = %q, want ready", got)
	}
}

func TestCancelTaskPreparationRemovesWorkspaceAndRow(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }

	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("canceled preparation row still exists")
	}
}

func TestCancelTaskPreparationDoesNotReuseStaleBranch(t *testing.T) {
	m, st, ws, repo := newGitTaskPreparationManager(t)
	project := st.projects["mer"]

	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, repo, branch)

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("new main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, repo, "add", "README.md")
	runManagerGit(t, repo, "commit", "-m", "advance main")
	info, err := ws.Create(context.Background(), ports.WorkspaceConfig{
		ProjectID: "mer", SessionID: "mer-1", Kind: domain.KindWorker,
		Branch: branch, BaseBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runManagerGit(t, info.Path, "rev-parse", "HEAD"), runManagerGit(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("recycled branch HEAD = %q, want current main %q", got, want)
	}
}

func newGitTaskPreparationManager(t *testing.T) (*Manager, *fakeStore, *gitworktree.Workspace, string) {
	t.Helper()
	repo := newManagerGitRepo(t)
	ws, err := gitworktree.New(gitworktree.Options{
		ManagedRoot:  t.TempDir(),
		RepoResolver: gitworktree.StaticRepoResolver{"mer": repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, st, _, _ := newManager()
	m.workspace = ws
	m.runBackground = func(work func()) { work() }
	project := st.projects["mer"]
	project.Path = repo
	project.Config.DefaultBranch = "main"
	st.projects["mer"] = project
	return m, st, ws, repo
}

func assertManagerBranchAbsent(t *testing.T, repo, branch string) {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).CombinedOutput()
	if err == nil {
		t.Fatalf("discarded preparation left branch %q behind", branch)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("inspect branch: %v: %s", err, out)
	}
}

func TestCancelTaskPreparationPreservesDirtyWorktree(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	rec := st.sessions["mer-1"]
	file := filepath.Join(rec.Metadata.WorkspacePath, "user-work.txt")
	if err := os.WriteFile(file, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); !errors.Is(err, ports.ErrWorkspaceDirty) {
		t.Fatalf("cancel error = %v, want dirty-worktree refusal", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("dirty worktree removed: %v", err)
	}
	if _, ok := st.sessions["mer-1"]; !ok {
		t.Fatal("dirty preparation lost its hidden row")
	}
	if got := runManagerGit(t, repo, "rev-parse", "refs/heads/"+rec.Metadata.Branch); got == "" {
		t.Fatal("dirty preparation lost its branch")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, repo, rec.Metadata.Branch)
}

func TestCancelTaskPreparationPreservesCommittedBranchButDoesNotReuseIt(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	project := st.projects["mer"]
	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	rec := st.sessions["mer-1"]
	if err := os.WriteFile(filepath.Join(rec.Metadata.WorkspacePath, "user-work.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, rec.Metadata.WorkspacePath, "add", "user-work.txt")
	runManagerGit(t, rec.Metadata.WorkspacePath, "commit", "-m", "user commit")
	committed := runManagerGit(t, rec.Metadata.WorkspacePath, "rev-parse", "HEAD")
	// Even if main later contains this commit, it was not part of the branch
	// when the preparation started and must not be pruned by cleanup.
	runManagerGit(t, repo, "merge", "--ff-only", rec.Metadata.Branch)
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if got := runManagerGit(t, repo, "rev-parse", "refs/heads/"+rec.Metadata.Branch); got != committed {
		t.Fatalf("committed branch moved or deleted: got %q, want %q", got, committed)
	}
	// SQLite assigns MAX(num)+1, so a deleted hidden row can reuse this ID.
	st.num = 0
	next, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	prepared := st.sessions["mer-1"]
	if prepared.Metadata.Branch != rec.Metadata.Branch+"-2" {
		t.Fatalf("new branch = %q, want suffix -2 after retained user branch", prepared.Metadata.Branch)
	}
	if got, want := runManagerGit(t, prepared.Metadata.WorkspacePath, "rev-parse", "HEAD"), runManagerGit(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("fresh task started at %q, want main %q", got, want)
	}
	if err := m.CancelTaskPreparation(context.Background(), next); err != nil {
		t.Fatal(err)
	}
}

func TestCancelWorkspaceProjectPreparationRemovesEachBranch(t *testing.T) {
	m, st, _, root := newGitTaskPreparationManager(t)
	child := newManagerGitRepo(t)
	childPath := filepath.Join(root, "api")
	if err := os.Rename(child, childPath); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, childPath, "remote", "add", "origin", childPath)
	runManagerGit(t, childPath, "fetch", "origin", "main")
	runManagerGit(t, childPath, "remote", "set-head", "origin", "main")
	assetPath := filepath.Join(root, "local-assets")
	if err := os.MkdirAll(assetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetPath, "note.txt"), []byte("copied by AO\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("api/\nlocal-assets/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManagerGit(t, root, "add", ".gitignore")
	runManagerGit(t, root, "commit", "-m", "ignore workspace children")
	project := st.projects["mer"]
	project.Kind = domain.ProjectKindWorkspace
	st.projects["mer"] = project
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{
		{Name: "api", RelativePath: "api", GitStatus: domain.GitStatusReady},
		{Name: "local-assets", RelativePath: "local-assets", GitStatus: domain.GitStatusNeedsInit},
	}

	token, err := m.PrepareTaskWorkspace(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if prep := m.taskPreparations[token]; prep.err != nil {
		t.Fatalf("prepare workspace project: %v", prep.err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	if _, err := os.Stat(filepath.Join(st.sessions["mer-1"].Metadata.WorkspacePath, "local-assets", "note.txt")); err != nil {
		t.Fatalf("prepared workspace missing copied asset: %v", err)
	}
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, root, branch)
	assertManagerBranchAbsent(t, childPath, branch)
}

func TestCancelTaskPreparationCanRetryAfterCleanupFailure(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }
	m.taskPreparationTTL = time.Hour
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	st.deletePrepErr = errors.New("database busy")
	if err := m.CancelTaskPreparation(context.Background(), token); err == nil {
		t.Fatal("cancel succeeded despite delete failure")
	}
	m.taskPreparationsMu.Lock()
	_, retained := m.taskPreparations[token]
	m.taskPreparationsMu.Unlock()
	if !retained {
		t.Fatal("failed cleanup consumed its retry handle")
	}

	st.deletePrepErr = nil
	if err := m.CancelTaskPreparation(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("workspace destroy calls = %d, want one successful cleanup followed by a row-only retry", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("retried cleanup left preparation row behind")
	}
}

func TestClaimedPreparationRollsBackWhenChatQueueFails(t *testing.T) {
	launcher := &recordingLauncher{queueErr: errors.New("queue unavailable")}
	m, st, _ := newChatManager(launcher)
	m.runBackground = func(work func()) { work() }
	ws := m.workspace.(*fakeWorkspace)

	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects[string(chatTestProject)])
	if err != nil {
		t.Fatal(err)
	}
	cfg := asyncChatSpawnConfig("do the thing")
	cfg.TaskPreparation = token
	if _, _, _, err := m.Spawn(context.Background(), cfg); err == nil {
		t.Fatal("spawn succeeded despite queue failure")
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("failed prepared spawn left its session row")
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
	}
}

func TestTaskPreparationExpires(t *testing.T) {
	m, st, _, ws := newManager()
	m.runBackground = func(work func()) { work() }
	m.taskPreparationTTL = 5 * time.Millisecond

	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.taskPreparationsMu.Lock()
		_, active := m.taskPreparations[token]
		m.taskPreparationsMu.Unlock()
		if !active {
			if _, ok := st.sessions["mer-1"]; ok {
				t.Fatal("expired preparation row still exists")
			}
			if ws.destroyed != 1 {
				t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("preparation did not expire")
}

func TestTaskPreparationExpiryDeletesUnmodifiedBranch(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	m.taskPreparationsMu.Lock()
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	m.taskPreparationTTL = time.Millisecond
	m.scheduleTaskPreparationCleanup(token, prep)
	m.taskPreparationsMu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.taskPreparationsMu.Lock()
		_, active := m.taskPreparations[token]
		m.taskPreparationsMu.Unlock()
		if !active {
			assertManagerBranchAbsent(t, repo, branch)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("preparation expiry did not finish cleanup")
}

func TestStartupCleansInterruptedTaskPreparation(t *testing.T) {
	m, st, _, ws := newManager()
	st.sessions["mer-1"] = domain.SessionRecord{
		ID:                "mer-1",
		ProjectID:         "mer",
		Kind:              domain.KindWorker,
		IsTaskPreparation: true,
		Metadata: domain.SessionMetadata{
			Branch:        "ao/mer-1/root",
			WorkspacePath: "/ws/mer-1",
		},
	}

	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.destroyed != 1 {
		t.Fatalf("destroy calls = %d, want 1", ws.destroyed)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("interrupted preparation row still exists")
	}
}

func TestStartupCleanupDeletesUnmodifiedBranch(t *testing.T) {
	m, st, _, repo := newGitTaskPreparationManager(t)
	token, err := m.PrepareTaskWorkspace(context.Background(), st.projects["mer"])
	if err != nil {
		t.Fatal(err)
	}
	branch := st.sessions["mer-1"].Metadata.Branch
	m.taskPreparationsMu.Lock()
	prep := m.taskPreparations[token]
	prep.timer.Stop()
	delete(m.taskPreparations, token)
	m.taskPreparationsMu.Unlock()
	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertManagerBranchAbsent(t, repo, branch)
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("startup cleanup left hidden reservation")
	}
}

func TestStartupDropsInterruptedPreparationWithoutCreatingWorkspace(t *testing.T) {
	m, st, _, ws := newManager()
	delete(st.projects, "mer")
	ws.createErr = errors.New("repository moved")
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true,
		Metadata:          domain.SessionMetadata{Branch: "ao/mer-1/root"},
	}

	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatalf("startup cleanup blocked daemon: %v", err)
	}
	if ws.createCount != 0 {
		t.Fatalf("workspace creates = %d, want zero", ws.createCount)
	}
	if _, ok := st.sessions["mer-1"]; ok {
		t.Fatal("interrupted preparation row still exists")
	}
}

func TestStartupCleanupFailureDoesNotBlockDaemon(t *testing.T) {
	m, st, _, ws := newManager()
	ws.destroyErr = errors.New("worktree is busy")
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		IsTaskPreparation: true,
		Metadata:          domain.SessionMetadata{Branch: "ao/mer-1/root", WorkspacePath: "/ws/mer-1"},
	}

	if err := m.CleanupInterruptedTaskPreparations(context.Background()); err != nil {
		t.Fatalf("cleanup failure blocked daemon startup: %v", err)
	}
	if _, ok := st.sessions["mer-1"]; !ok {
		t.Fatal("failed cleanup lost the hidden row needed for a later retry")
	}
}
