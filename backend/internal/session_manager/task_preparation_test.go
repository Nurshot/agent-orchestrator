package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

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
