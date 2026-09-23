package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// TestArchiveDiskE2E fills a scratch AO data directory with dirty sessions up
// to the 5 GB allowance, archives every session, and records how big that
// directory is. It does this twice. The first run follows the archive behavior
// on main: a dirty worktree is left in place and the session is marked
// terminated. The second run calls the current session-manager Kill, which is
// the path the archive button uses.
//
// Set AO_DISK_E2E=1 to run it. AO_DISK_E2E_SESSIONS and AO_DISK_E2E_MB tune the
// population. Their product is capped at 4800 MiB so the live data directory
// stays under 5 GB. The scratch directory is removed before the second run, so
// the two populations are not on disk together. This test never reads or
// writes ~/.ao.
func TestArchiveDiskE2E(t *testing.T) {
	if os.Getenv("AO_DISK_E2E") == "" {
		t.Skip("set AO_DISK_E2E=1 to populate a scratch AO data dir and archive it")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	sessions := diskEnvInt(t, "AO_DISK_E2E_SESSIONS", 20)
	payloadMB := diskEnvInt(t, "AO_DISK_E2E_MB", 240)
	if sessions < 1 || payloadMB < 1 {
		t.Fatal("session and payload counts must be at least 1")
	}
	if sessions*payloadMB > 4800 {
		t.Fatalf("population is %d MiB, cap is 4800 MiB", sessions*payloadMB)
	}

	root := t.TempDir()
	before := measureArchivePopulation(t, git, filepath.Join(root, "before"), sessions, payloadMB, archiveLikeMain)
	if err := os.RemoveAll(filepath.Join(root, "before")); err != nil {
		t.Fatal(err)
	}
	after := measureArchivePopulation(t, git, filepath.Join(root, "after"), sessions, payloadMB, archiveLikeFix)

	t.Logf("sessions=%d ignored_each=%d MiB", sessions, payloadMB)
	t.Logf("BEFORE FIX  live ao=%s worktrees=%s db=%s git=%s",
		fmtDisk(before.live.ao), fmtDisk(before.live.worktrees), fmtDisk(before.live.db), fmtDisk(before.live.git))
	t.Logf("BEFORE FIX  archived ao=%s worktrees=%s db=%s git=%s",
		fmtDisk(before.archived.ao), fmtDisk(before.archived.worktrees), fmtDisk(before.archived.db), fmtDisk(before.archived.git))
	t.Logf("BEFORE FIX  restored ao=%s worktrees=%s db=%s git=%s dirty=%d payload=%d edits=%d head_moved=%d",
		fmtDisk(before.restored.ao), fmtDisk(before.restored.worktrees), fmtDisk(before.restored.db), fmtDisk(before.restored.git),
		before.state.dirty, before.state.payload, before.state.edits, before.state.headMoved)
	t.Logf("AFTER FIX   live ao=%s worktrees=%s db=%s git=%s",
		fmtDisk(after.live.ao), fmtDisk(after.live.worktrees), fmtDisk(after.live.db), fmtDisk(after.live.git))
	t.Logf("AFTER FIX   archived ao=%s worktrees=%s db=%s git=%s",
		fmtDisk(after.archived.ao), fmtDisk(after.archived.worktrees), fmtDisk(after.archived.db), fmtDisk(after.archived.git))
	t.Logf("AFTER FIX   restored ao=%s worktrees=%s db=%s git=%s dirty=%d payload=%d edits=%d head_moved=%d",
		fmtDisk(after.restored.ao), fmtDisk(after.restored.worktrees), fmtDisk(after.restored.db), fmtDisk(after.restored.git),
		after.state.dirty, after.state.payload, after.state.edits, after.state.headMoved)

	if before.archived.worktrees*10 < before.live.worktrees*9 {
		t.Fatalf("old archive shrank worktrees from %d to %d; a dirty folder should stay", before.live.worktrees, before.archived.worktrees)
	}
	reclaimed := after.live.ao - after.archived.ao
	if reclaimed < after.live.worktrees*8/10 {
		t.Fatalf("new archive reclaimed %d bytes from an ao dir whose worktrees were %d", reclaimed, after.live.worktrees)
	}
	if after.archived.worktrees > 1<<20 {
		t.Fatalf("new archive left %d bytes of worktrees", after.archived.worktrees)
	}
	if before.state.dirty != sessions || before.state.payload != sessions || before.state.edits != sessions || before.state.headMoved != 0 {
		t.Fatalf("old restore state = %+v, want every session dirty with edits and payload, head unmoved", before.state)
	}
	if before.restored.worktrees*10 < before.live.worktrees*9 {
		t.Fatalf("old restore shrank worktrees from %d to %d", before.live.worktrees, before.restored.worktrees)
	}
	if after.state.dirty != 0 || after.state.payload != 0 || after.state.edits != 0 || after.state.headMoved != 0 || after.state.clean != sessions {
		t.Fatalf("new restore state = %+v, want a clean branch checkout with no edits and no payload", after.state)
	}
	if after.restored.worktrees > int64(sessions)<<20 {
		t.Fatalf("new restore brought worktrees back to %d bytes", after.restored.worktrees)
	}
}

type archiveMode int

const (
	archiveLikeMain archiveMode = iota
	archiveLikeFix
)

type diskSnapshot struct {
	ao        int64
	worktrees int64
	db        int64
	git       int64
}

type archiveMeasurement struct {
	live     diskSnapshot
	archived diskSnapshot
	restored diskSnapshot
	state    restoredRepoState
}

type restoredRepoState struct {
	clean     int
	dirty     int
	payload   int
	edits     int
	headMoved int
}

func measureArchivePopulation(t *testing.T, git, root string, sessions, payloadMB int, mode archiveMode) archiveMeasurement {
	t.Helper()
	ctx := context.Background()
	dataDir := filepath.Join(root, "ao")
	repo := setupDiskProjectRepo(t, git, filepath.Join(root, "project"))
	store, err := sqlitetest.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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

	type seeded struct {
		id   domain.SessionID
		info ports.WorkspaceInfo
		head string
	}
	seededSessions := make([]seeded, 0, sessions)
	for i := 0; i < sessions; i++ {
		rec, err := store.CreateSession(ctx, domain.SessionRecord{
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
		info, err := ws.Create(ctx, ports.WorkspaceConfig{
			ProjectID: "diskproj",
			SessionID: rec.ID,
			Branch:    "feature/" + string(rec.ID),
		})
		if err != nil {
			t.Fatalf("create worktree %s: %v", rec.ID, err)
		}
		if err := os.WriteFile(filepath.Join(info.Path, "README.md"), []byte("edited by agent\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(info.Path, "notes.txt"), []byte("new untracked work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeDiskPayload(filepath.Join(info.Path, "node_modules", "payload.bin"), payloadMB); err != nil {
			t.Fatal(err)
		}
		rec.Metadata.WorkspacePath = info.Path
		rec.Metadata.Branch = info.Branch
		rec.Metadata.WorkspaceRepoPath = repo
		if err := store.UpdateSession(ctx, rec); err != nil {
			t.Fatal(err)
		}
		seededSessions = append(seededSessions, seeded{
			id:   rec.ID,
			info: info,
			head: diskGitOutput(t, git, info.Path, "rev-parse", "HEAD"),
		})
	}

	live := takeDiskSnapshot(t, dataDir, repo)
	for _, session := range seededSessions {
		switch mode {
		case archiveLikeMain:
			err := ws.Destroy(ctx, session.info)
			if !errors.Is(err, ports.ErrWorkspaceDirty) {
				t.Fatalf("old archive %s: %v", session.id, err)
			}
			if err := store.DeleteSessionWorktrees(ctx, session.id); err != nil {
				t.Fatal(err)
			}
			if err := lcm.MarkTerminated(ctx, session.id); err != nil {
				t.Fatalf("terminate %s: %v", session.id, err)
			}
			if _, err := os.Stat(session.info.Path); err != nil {
				t.Fatalf("old archive removed %s: %v", session.info.Path, err)
			}
		case archiveLikeFix:
			freed, err := mgr.Kill(ctx, session.id)
			if err != nil {
				t.Fatalf("kill %s: %v", session.id, err)
			}
			if !freed {
				t.Fatalf("kill %s did not remove the worktree", session.id)
			}
			preserved, saveFailed := mgr.LastArchiveNotice(session.id)
			if !preserved || saveFailed {
				t.Fatalf("kill %s preserved=%v saveFailed=%v", session.id, preserved, saveFailed)
			}
			if _, err := os.Stat(session.info.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("worktree %s still present after archive", session.info.Path)
			}
		}
	}
	archived := takeDiskSnapshot(t, dataDir, repo)
	var state restoredRepoState
	for _, session := range seededSessions {
		restored, err := ws.Restore(ctx, ports.WorkspaceConfig{
			ProjectID: "diskproj",
			SessionID: session.id,
			Branch:    session.info.Branch,
			Path:      session.info.Path,
			RepoPath:  repo,
		})
		if err != nil {
			t.Fatalf("restore %s: %v", session.id, err)
		}
		inspectRestoredWorktree(t, git, restored.Path, session.head, &state)
	}
	return archiveMeasurement{
		live:     live,
		archived: archived,
		restored: takeDiskSnapshot(t, dataDir, repo),
		state:    state,
	}
}

func inspectRestoredWorktree(t *testing.T, git, path, head string, state *restoredRepoState) {
	t.Helper()
	if got := diskGitOutput(t, git, path, "rev-parse", "HEAD"); got != head {
		state.headMoved++
	}
	if diskGitOutput(t, git, path, "status", "--porcelain") == "" {
		state.clean++
	} else {
		state.dirty++
	}
	if _, err := os.Stat(filepath.Join(path, "node_modules", "payload.bin")); err == nil {
		state.payload++
	}
	readme, err := os.ReadFile(filepath.Join(path, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	notes, notesErr := os.ReadFile(filepath.Join(path, "notes.txt"))
	if string(readme) == "edited by agent\n" && notesErr == nil && string(notes) == "new untracked work\n" {
		state.edits++
	}
}

func setupDiskProjectRepo(t *testing.T, git, dir string) string {
	t.Helper()
	origin := filepath.Join(dir, "origin.git")
	seed := filepath.Join(dir, "seed")
	repo := filepath.Join(dir, "repo")
	diskGit(t, git, "", "init", "--bare", origin)
	diskGit(t, git, "", "init", seed)
	diskGit(t, git, seed, "config", "user.email", "ao@example.com")
	diskGit(t, git, seed, "config", "user.name", "Ao Agents")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diskGit(t, git, seed, "add", "README.md", ".gitignore")
	diskGit(t, git, seed, "commit", "-m", "seed")
	diskGit(t, git, seed, "branch", "-M", "main")
	diskGit(t, git, seed, "remote", "add", "origin", origin)
	diskGit(t, git, seed, "push", "-u", "origin", "main")
	diskGit(t, git, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	diskGit(t, git, "", "clone", origin, repo)
	diskGit(t, git, repo, "config", "user.email", "ao@example.com")
	diskGit(t, git, repo, "config", "user.name", "Ao Agents")
	return repo
}

func diskGitOutput(t *testing.T, git, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func diskGit(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command(git, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", args, err, out)
	}
}

func writeDiskPayload(path string, megabytes int) error {
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

func takeDiskSnapshot(t *testing.T, dataDir, repo string) diskSnapshot {
	t.Helper()
	return diskSnapshot{
		ao:        diskBytes(t, dataDir),
		worktrees: diskBytes(t, filepath.Join(dataDir, "worktrees")),
		db: diskBytes(t, filepath.Join(dataDir, "ao.db")) +
			diskBytes(t, filepath.Join(dataDir, "ao.db-wal")) +
			diskBytes(t, filepath.Join(dataDir, "ao.db-shm")),
		git: diskBytes(t, filepath.Join(repo, ".git")),
	}
}

func diskBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
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

func diskEnvInt(t *testing.T, key string, fallback int) int {
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

func fmtDisk(n int64) string {
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
