package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestArchiveDirtyWorktreeDiskBeforeAfter measures the archive path on real
// git worktrees. Each worktree holds a small uncommitted edit plus a
// gitignored payload, the stand-in for an install such as node_modules.
//
// Before is the old outcome: the session is archived and the dirty folder
// stays. After is snapshot-then-remove: the edit is a private git ref, and
// the folder, including the ignored payload, is gone.
//
// The default is one worktree and 4 MiB, small enough for CI. A larger run
// sets AO_DISK_BENCH_WORKTREES and AO_DISK_BENCH_MB. Their product is capped
// at 4 GiB so a bench stays inside a 5 GiB allowance.
func TestArchiveDirtyWorktreeDiskBeforeAfter(t *testing.T) {
	git := requireGit(t)
	worktrees := envInt(t, "AO_DISK_BENCH_WORKTREES", 1)
	payloadMB := envInt(t, "AO_DISK_BENCH_MB", 4)
	if worktrees < 1 || payloadMB < 1 {
		t.Fatal("bench counts must be at least 1")
	}
	if worktrees*payloadMB > 4*1024 {
		t.Fatalf("bench is %d MiB, cap is 4096 MiB", worktrees*payloadMB)
	}

	tmp := t.TempDir()
	repo := setupOriginClone(t, git, tmp)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	runGit(t, git, repo, "add", ".gitignore")
	runGit(t, git, repo, "commit", "-m", "ignore installs")
	// New session branches are cut from origin/main, so the ignore rule has to
	// be on the remote before the worktrees are created.
	runGit(t, git, repo, "push", "origin", "HEAD:main")

	root := filepath.Join(tmp, "managed")
	ws, err := New(Options{Binary: git, ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()

	type archived struct {
		info ports.WorkspaceInfo
		cfg  ports.WorkspaceConfig
		ref  string
	}
	sessions := make([]archived, 0, worktrees)
	for i := 0; i < worktrees; i++ {
		cfg := ports.WorkspaceConfig{
			ProjectID: "proj",
			SessionID: domain.SessionID(fmt.Sprintf("disk-%d", i)),
			Branch:    fmt.Sprintf("feature/disk-%d", i),
		}
		info, err := ws.Create(ctx, cfg)
		if err != nil {
			t.Fatalf("create %s: %v", cfg.SessionID, err)
		}
		if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("edited by agent\n"), 0o644); err != nil {
			t.Fatalf("edit README: %v", err)
		}
		if err := os.WriteFile(filepath.Join(info.Path, "notes.txt"), []byte("new untracked work\n"), 0o644); err != nil {
			t.Fatalf("write notes: %v", err)
		}
		if err := writeIgnoredPayload(filepath.Join(info.Path, "node_modules", "payload.bin"), payloadMB); err != nil {
			t.Fatalf("write payload: %v", err)
		}
		sessions = append(sessions, archived{info: info, cfg: cfg})
	}

	gitBefore := dirBytes(t, filepath.Join(repo, ".git"))
	checkoutsBefore := dirBytes(t, root)
	t.Logf("before  checkouts=%s git=%s  (%d worktrees, %d MiB ignored each)",
		fmtBytes(checkoutsBefore), fmtBytes(gitBefore), worktrees, payloadMB)

	for i := range sessions {
		ref, err := ws.StashUncommitted(ctx, sessions[i].info)
		if err != nil {
			t.Fatalf("stash %s: %v", sessions[i].cfg.SessionID, err)
		}
		if ref == "" {
			t.Fatalf("stash %s returned an empty ref", sessions[i].cfg.SessionID)
		}
		sessions[i].ref = ref
		if err := ws.ForceDestroy(ctx, sessions[i].info); err != nil {
			t.Fatalf("force destroy %s: %v", sessions[i].cfg.SessionID, err)
		}
		if _, err := os.Stat(sessions[i].info.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("worktree %s still on disk after archive", sessions[i].info.Path)
		}
	}

	gitAfter := dirBytes(t, filepath.Join(repo, ".git"))
	checkoutsAfter := dirBytes(t, root)
	gitDelta := gitAfter - gitBefore
	gitGrowth := gitDelta
	if gitGrowth < 0 {
		gitGrowth = 0
	}
	reclaimed := checkoutsBefore - checkoutsAfter
	t.Logf("after   checkouts=%s git=%s git_delta=%s reclaimed=%s",
		fmtBytes(checkoutsAfter), fmtBytes(gitAfter), fmtBytes(gitDelta), fmtBytes(reclaimed))

	if checkoutsAfter > checkoutsBefore/10 {
		t.Fatalf("checkouts after archive = %d bytes, before = %d; the folders should be gone", checkoutsAfter, checkoutsBefore)
	}
	// The ignored payload must not be copied into the object database.
	// Snapshot objects are the README edit and notes.txt only.
	if gitGrowth > reclaimed/5 {
		t.Fatalf("git grew by %d bytes while reclaiming %d; the snapshot stored ignored files", gitGrowth, reclaimed)
	}

	restored, err := ws.Restore(ctx, sessions[0].cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := ws.ApplyPreserved(ctx, restored, sessions[0].ref); err != nil {
		t.Fatalf("apply: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(restored.Path, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(readme) != "edited by agent\n" {
		t.Fatalf("README = %q", readme)
	}
	notes, err := os.ReadFile(filepath.Join(restored.Path, "notes.txt"))
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	if string(notes) != "new untracked work\n" {
		t.Fatalf("notes = %q", notes)
	}
	if _, err := os.Stat(filepath.Join(restored.Path, "node_modules", "payload.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ignored payload came back; the snapshot must not contain it")
	}
	back := dirBytes(t, restored.Path)
	t.Logf("reapply worktree=%s (source edits only, ignored payload absent)", fmtBytes(back))
	if back > 1024*1024 {
		t.Fatalf("reapplied worktree is %d bytes; ignored payload was stored in the snapshot", back)
	}
}

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s = %q: %v", key, raw, err)
	}
	return n
}

func writeIgnoredPayload(path string, megabytes int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	block := make([]byte, 1<<20)
	for i := range block {
		block[i] = byte(i)
	}
	for range megabytes {
		if _, err := f.Write(block); err != nil {
			return err
		}
	}
	return f.Close()
}

func dirBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("size %s: %v", root, err)
	}
	return total
}

func fmtBytes(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%s%.2f GiB", sign, float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%s%.1f MiB", sign, float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%s%d B", sign, n)
	}
}
