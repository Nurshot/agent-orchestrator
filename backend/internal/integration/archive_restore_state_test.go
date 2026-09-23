package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// TestArchiveRestoreRepoStates walks the archive, restore, and reapply path
// across repository shapes that a dirty session actually has: a tracked edit,
// a deletion, a rename, a mode change, a symlink, a nested new file, an empty
// file, a non-utf8 file, a unicode name, and a gitignored payload. Restore
// must come back on the same commit with a clean tree. Reapply must put the
// source edits back without a commit and without the ignored payload.
func TestArchiveRestoreRepoStates(t *testing.T) {
	git := diskRequireGit(t)
	ctx := context.Background()
	env := newRestoreStateEnv(t, git)

	t.Run("mixed tree", func(t *testing.T) {
		const branch = "feature/mixed"
		info, head := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "README.md"), []byte("edited by agent\n"))
			mustWrite(t, filepath.Join(path, "notes.txt"), []byte("new untracked work\n"))
			mustWrite(t, filepath.Join(path, "empty.txt"), nil)
			mustWrite(t, filepath.Join(path, "café notes.txt"), []byte("unicode name\n"))
			mustWrite(t, filepath.Join(path, "data.bin"), []byte{0, 1, 2, 255})
			mustWrite(t, filepath.Join(path, "dir", "sub", "nested.txt"), []byte("nested\n"))
			if err := os.Remove(filepath.Join(path, "KEEP.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(path, "OLD.txt"), filepath.Join(path, "NEW.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(path, "script.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("README.md", filepath.Join(path, "alias")); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(path, "node_modules", "payload.bin"), []byte("ignored-bulk"))
		})
		env.kill(t, info)
		restored := env.restore(t, info)
		assertHead(t, git, restored.Path, head)
		assertClean(t, git, restored.Path)
		if _, err := os.Stat(filepath.Join(restored.Path, "KEEP.txt")); err != nil {
			t.Fatalf("restore dropped a tracked file: %v", err)
		}
		if _, err := os.Stat(filepath.Join(restored.Path, "notes.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("restore brought back an untracked file")
		}
		if _, err := os.Stat(filepath.Join(restored.Path, "node_modules", "payload.bin")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("restore brought back an ignored payload")
		}

		result := env.reapply(t, info.SessionID)
		if result.Conflicts {
			t.Fatal("clean reapply reported conflicts")
		}
		assertHead(t, git, restored.Path, head)
		assertNoCherryPick(t, git, restored.Path)
		assertFile(t, filepath.Join(restored.Path, "README.md"), "edited by agent\n")
		assertFile(t, filepath.Join(restored.Path, "notes.txt"), "new untracked work\n")
		assertFile(t, filepath.Join(restored.Path, "empty.txt"), "")
		assertFile(t, filepath.Join(restored.Path, "café notes.txt"), "unicode name\n")
		assertFile(t, filepath.Join(restored.Path, "data.bin"), string([]byte{0, 1, 2, 255}))
		assertFile(t, filepath.Join(restored.Path, "dir", "sub", "nested.txt"), "nested\n")
		assertFile(t, filepath.Join(restored.Path, "NEW.txt"), "old\n")
		if _, err := os.Stat(filepath.Join(restored.Path, "KEEP.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("reapply left a deleted file in place")
		}
		if _, err := os.Stat(filepath.Join(restored.Path, "OLD.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("reapply left a renamed file at the old path")
		}
		target, err := os.Readlink(filepath.Join(restored.Path, "alias"))
		if err != nil || target != "README.md" {
			t.Fatalf("symlink = %q, %v", target, err)
		}
		mode := diskGitOutput(t, git, restored.Path, "ls-files", "-s", "script.sh")
		if len(mode) < 7 || mode[:7] != "100755 " {
			t.Fatalf("script.sh mode = %q, want 100755", mode)
		}
		if _, err := os.Stat(filepath.Join(restored.Path, "node_modules", "payload.bin")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("reapply restored an ignored payload")
		}
		if _, err := env.mgr.ReapplyPreservedEdits(ctx, info.SessionID); !errors.Is(err, sessionmanager.ErrNoPreservedEdits) {
			t.Fatalf("second reapply = %v, want no preserved edits", err)
		}
	})

	t.Run("branch moved without overlap", func(t *testing.T) {
		const branch = "feature/moved"
		info, head := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "notes.txt"), []byte("from the agent\n"))
		})
		env.kill(t, info)
		diskGit(t, git, env.repo, "switch", branch)
		mustWrite(t, filepath.Join(env.repo, "OTHER.md"), []byte("landed later\n"))
		diskGit(t, git, env.repo, "add", "OTHER.md")
		diskGit(t, git, env.repo, "commit", "-m", "later work")
		later := diskGitOutput(t, git, env.repo, "rev-parse", "HEAD")
		diskGit(t, git, env.repo, "switch", "main")
		if later == head {
			t.Fatal("branch did not move")
		}
		result := env.reapply(t, info.SessionID)
		if result.Conflicts {
			t.Fatal("disjoint later commit conflicted with the saved edit")
		}
		restored := info.Path
		assertHead(t, git, restored, later)
		assertFile(t, filepath.Join(restored, "notes.txt"), "from the agent\n")
		assertFile(t, filepath.Join(restored, "OTHER.md"), "landed later\n")
		assertNoCherryPick(t, git, restored)
	})

	t.Run("same lines committed later", func(t *testing.T) {
		const branch = "feature/overlap"
		info, head := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "README.md"), []byte("edited by agent\n"))
		})
		env.kill(t, info)
		diskGit(t, git, env.repo, "switch", branch)
		mustWrite(t, filepath.Join(env.repo, "README.md"), []byte("edited by someone else\n"))
		diskGit(t, git, env.repo, "add", "README.md")
		diskGit(t, git, env.repo, "commit", "-m", "overlap")
		later := diskGitOutput(t, git, env.repo, "rev-parse", "HEAD")
		diskGit(t, git, env.repo, "switch", "main")
		result := env.reapply(t, info.SessionID)
		if !result.Conflicts {
			t.Fatal("overlapping edit applied cleanly")
		}
		assertHead(t, git, info.Path, later)
		if later == head {
			t.Fatal("conflict moved HEAD backwards")
		}
		readme, err := os.ReadFile(filepath.Join(info.Path, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(readme)
		if !strings.Contains(text, "edited by agent") || !strings.Contains(text, "edited by someone else") || !strings.Contains(text, "<<<<<<<") {
			t.Fatalf("README = %q, want both sides and a conflict marker", text)
		}
		ref := "refs/ao/preserved/" + string(info.SessionID)
		if diskGitOutput(t, git, env.repo, "rev-parse", ref) == "" {
			t.Fatal("conflict deleted the snapshot ref")
		}
		assertNoCherryPick(t, git, info.Path)
		again, err := env.mgr.ReapplyPreservedEdits(ctx, info.SessionID)
		if err != nil {
			t.Fatalf("second reapply after conflict: %v", err)
		}
		if !again.Conflicts {
			t.Fatal("second reapply after conflict reported a clean apply")
		}
		assertHead(t, git, info.Path, later)
	})

	t.Run("saved edit already committed", func(t *testing.T) {
		const branch = "feature/already"
		info, _ := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "README.md"), []byte("edited by agent\n"))
			mustWrite(t, filepath.Join(path, "notes.txt"), []byte("new untracked work\n"))
		})
		env.kill(t, info)
		diskGit(t, git, env.repo, "switch", branch)
		mustWrite(t, filepath.Join(env.repo, "README.md"), []byte("edited by agent\n"))
		mustWrite(t, filepath.Join(env.repo, "notes.txt"), []byte("new untracked work\n"))
		diskGit(t, git, env.repo, "add", "README.md", "notes.txt")
		diskGit(t, git, env.repo, "commit", "-m", "the agent work, committed later")
		later := diskGitOutput(t, git, env.repo, "rev-parse", "HEAD")
		diskGit(t, git, env.repo, "switch", "main")
		result := env.reapply(t, info.SessionID)
		if result.Conflicts {
			t.Fatal("reapply conflicted with a commit that already contains the saved edits")
		}
		assertHead(t, git, info.Path, later)
		assertClean(t, git, info.Path)
		assertFile(t, filepath.Join(info.Path, "notes.txt"), "new untracked work\n")
		assertNoCherryPick(t, git, info.Path)
	})

	t.Run("local edits before reapply", func(t *testing.T) {
		const branch = "feature/local"
		info, head := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "README.md"), []byte("edited by agent\n"))
			mustWrite(t, filepath.Join(path, "notes.txt"), []byte("from the agent\n"))
		})
		env.kill(t, info)
		restored := env.restore(t, info)
		mustWrite(t, filepath.Join(restored.Path, "KEEP.txt"), []byte("typed after restore\n"))
		mustWrite(t, filepath.Join(restored.Path, "scratch.txt"), []byte("also after restore\n"))
		result := env.reapply(t, info.SessionID)
		if result.Conflicts {
			t.Fatal("unrelated local edits conflicted with the saved snapshot")
		}
		assertHead(t, git, restored.Path, head)
		assertFile(t, filepath.Join(restored.Path, "README.md"), "edited by agent\n")
		assertFile(t, filepath.Join(restored.Path, "notes.txt"), "from the agent\n")
		assertFile(t, filepath.Join(restored.Path, "KEEP.txt"), "typed after restore\n")
		assertFile(t, filepath.Join(restored.Path, "scratch.txt"), "also after restore\n")
		assertNoCherryPick(t, git, restored.Path)
	})

	t.Run("local edit overlaps snapshot", func(t *testing.T) {
		const branch = "feature/local-overlap"
		info, head := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "README.md"), []byte("edited by agent\n"))
		})
		env.kill(t, info)
		restored := env.restore(t, info)
		mustWrite(t, filepath.Join(restored.Path, "README.md"), []byte("typed after restore\n"))
		result := env.reapply(t, info.SessionID)
		if !result.Conflicts {
			t.Fatal("overlapping local edit applied over the person's newer text")
		}
		assertHead(t, git, restored.Path, head)
		readme, err := os.ReadFile(filepath.Join(restored.Path, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(readme)
		if !strings.Contains(text, "edited by agent") || !strings.Contains(text, "typed after restore") || !strings.Contains(text, "<<<<<<<") {
			t.Fatalf("README = %q, want both edits and a conflict marker", text)
		}
		assertNoCherryPick(t, git, restored.Path)
	})

	t.Run("file replaced by a directory", func(t *testing.T) {
		const branch = "feature/typechange"
		info, head := env.dirtySession(t, branch, func(path string) {
			if err := os.Remove(filepath.Join(path, "OLD.txt")); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(path, "OLD.txt", "inside.txt"), []byte("now a directory\n"))
		})
		env.kill(t, info)
		restored := env.restore(t, info)
		if _, err := os.Stat(filepath.Join(restored.Path, "OLD.txt")); err != nil {
			t.Fatalf("restore lost OLD.txt: %v", err)
		}
		result := env.reapply(t, info.SessionID)
		if result.Conflicts {
			t.Fatal("replacing a file with a directory conflicted")
		}
		assertHead(t, git, restored.Path, head)
		assertFile(t, filepath.Join(restored.Path, "OLD.txt", "inside.txt"), "now a directory\n")
		infoOld, err := os.Lstat(filepath.Join(restored.Path, "OLD.txt"))
		if err != nil || !infoOld.IsDir() {
			t.Fatalf("OLD.txt is not a directory after reapply: %v", err)
		}
	})

	t.Run("branch checked out in the main worktree", func(t *testing.T) {
		const branch = "feature/busy"
		info, _ := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "notes.txt"), []byte("keep this\n"))
		})
		env.kill(t, info)
		diskGit(t, git, env.repo, "switch", branch)
		t.Cleanup(func() {
			_ = exec.Command(git, "-C", env.repo, "switch", "main").Run()
		})
		_, err := env.mgr.ReapplyPreservedEdits(ctx, info.SessionID)
		if !errors.Is(err, ports.ErrWorkspaceBranchCheckedOutElsewhere) {
			t.Fatalf("reapply while branch is checked out elsewhere = %v", err)
		}
		if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("busy branch still created a second worktree")
		}
		ref := "refs/ao/preserved/" + string(info.SessionID)
		if diskGitOutput(t, git, env.repo, "rev-parse", "--verify", ref) == "" {
			t.Fatal("busy branch deleted the snapshot")
		}
		diskGit(t, git, env.repo, "switch", "main")
		result := env.reapply(t, info.SessionID)
		if result.Conflicts {
			t.Fatal("reapply after freeing the branch conflicted")
		}
		assertFile(t, filepath.Join(info.Path, "notes.txt"), "keep this\n")
	})

	t.Run("missing branch", func(t *testing.T) {
		const branch = "feature/missing"
		info, _ := env.dirtySession(t, branch, func(path string) {
			mustWrite(t, filepath.Join(path, "notes.txt"), []byte("keep this\n"))
		})
		env.kill(t, info)
		diskGit(t, git, env.repo, "branch", "-D", branch)
		_, err := env.mgr.ReapplyPreservedEdits(ctx, info.SessionID)
		if !errors.Is(err, ports.ErrSessionBranchMissing) {
			t.Fatalf("reapply missing branch = %v", err)
		}
		if _, err := os.Stat(info.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing branch still created a worktree")
		}
		ref := "refs/ao/preserved/" + string(info.SessionID)
		if diskGitOutput(t, git, env.repo, "rev-parse", "--verify", ref) == "" {
			t.Fatal("missing branch deleted the snapshot")
		}
	})
}

type restoreStateEnv struct {
	ctx   context.Context
	git   string
	repo  string
	ws    *gitworktree.Workspace
	store interface {
		CreateSession(context.Context, domain.SessionRecord) (domain.SessionRecord, error)
		UpdateSession(context.Context, domain.SessionRecord) error
	}
	mgr *sessionmanager.Manager
}

func newRestoreStateEnv(t *testing.T, git string) *restoreStateEnv {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	repo := setupDiskProjectRepo(t, git, filepath.Join(root, "project"))
	mustWrite(t, filepath.Join(repo, "KEEP.txt"), []byte("keep\n"))
	mustWrite(t, filepath.Join(repo, "OLD.txt"), []byte("old\n"))
	mustWrite(t, filepath.Join(repo, "script.sh"), []byte("#!/bin/sh\necho hi\n"))
	diskGit(t, git, repo, "add", "KEEP.txt", "OLD.txt", "script.sh")
	diskGit(t, git, repo, "commit", "-m", "tracked shapes")
	diskGit(t, git, repo, "push", "origin", "HEAD:main")

	dataDir := filepath.Join(root, "ao")
	store, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:           "diskproj",
		Path:         repo,
		RegisteredAt: time.Now().UTC(),
		Config: domain.ProjectConfig{
			Worker: domain.RoleOverride{Harness: domain.HarnessClaudeCode},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ws, err := gitworktree.New(gitworktree.Options{
		Binary:       git,
		ManagedRoot:  filepath.Join(dataDir, "worktrees"),
		RepoResolver: gitworktree.StaticRepoResolver{"diskproj": repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	lcm := lifecycle.New(store, &captureMessenger{})
	mgr := sessionmanager.New(sessionmanager.Deps{
		Runtime:   &stubRuntime{},
		Agents:    stubAgents{},
		Workspace: ws,
		Store:     store,
		Lifecycle: lcm,
		DataDir:   dataDir,
		LookPath:  func(string) (string, error) { return "/usr/bin/true", nil },
	})
	return &restoreStateEnv{ctx: ctx, git: git, repo: repo, ws: ws, store: store, mgr: mgr}
}

func (e *restoreStateEnv) dirtySession(t *testing.T, branch string, dirty func(string)) (ports.WorkspaceInfo, string) {
	t.Helper()
	rec, err := e.store.CreateSession(e.ctx, domain.SessionRecord{
		ProjectID: "diskproj",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessClaudeCode,
		Mode:      domain.SessionModeTUI,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: time.Now().UTC()},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.ws.Create(e.ctx, ports.WorkspaceConfig{
		ProjectID: "diskproj",
		SessionID: rec.ID,
		Branch:    branch,
	})
	if err != nil {
		t.Fatal(err)
	}
	dirty(info.Path)
	head := diskGitOutput(t, e.git, info.Path, "rev-parse", "HEAD")
	rec.Metadata.WorkspacePath = info.Path
	rec.Metadata.Branch = info.Branch
	rec.Metadata.WorkspaceRepoPath = e.repo
	if err := e.store.UpdateSession(e.ctx, rec); err != nil {
		t.Fatal(err)
	}
	info.SessionID = rec.ID
	info.ProjectID = "diskproj"
	info.RepoPath = e.repo
	return info, head
}

func (e *restoreStateEnv) kill(t *testing.T, info ports.WorkspaceInfo) {
	t.Helper()
	freed, err := e.mgr.Kill(e.ctx, info.SessionID)
	if err != nil || !freed {
		t.Fatalf("kill %s freed=%v err=%v", info.SessionID, freed, err)
	}
}

func (e *restoreStateEnv) restore(t *testing.T, info ports.WorkspaceInfo) ports.WorkspaceInfo {
	t.Helper()
	restored, err := e.ws.Restore(e.ctx, ports.WorkspaceConfig{
		ProjectID: info.ProjectID,
		SessionID: info.SessionID,
		Branch:    info.Branch,
		Path:      info.Path,
		RepoPath:  info.RepoPath,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	return restored
}

func (e *restoreStateEnv) reapply(t *testing.T, id domain.SessionID) sessionmanager.ReapplyResult {
	t.Helper()
	result, err := e.mgr.ReapplyPreservedEdits(e.ctx, id)
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	return result
}

func diskRequireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	return git
}

func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertHead(t *testing.T, git, path, head string) {
	t.Helper()
	if got := diskGitOutput(t, git, path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
}

func assertClean(t *testing.T, git, path string) {
	t.Helper()
	if got := diskGitOutput(t, git, path, "status", "--porcelain"); got != "" {
		t.Fatalf("restore status = %q, want clean", got)
	}
}

func assertNoCherryPick(t *testing.T, git, path string) {
	t.Helper()
	rel := diskGitOutput(t, git, path, "rev-parse", "--git-path", "CHERRY_PICK_HEAD")
	abs := rel
	if !filepath.IsAbs(rel) {
		abs = filepath.Join(path, rel)
	}
	if _, err := os.Stat(abs); err == nil {
		t.Fatalf("cherry-pick still in progress at %s", abs)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
