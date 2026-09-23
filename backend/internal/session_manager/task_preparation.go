package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const defaultTaskPreparationTTL = 5 * time.Minute

// taskPreparation is the daemon-owned speculative worktree behind one opaque
// modal token. The session row reserves the final id and stays hidden until a
// spawn claims it.
type taskPreparation struct {
	record  domain.SessionRecord
	project domain.ProjectRecord
	done    chan struct{}
	cancel  context.CancelFunc
	timer   *time.Timer

	workspace        ports.WorkspaceInfo
	workspaceProject *ports.WorkspaceProjectInfo
	err              error
	promoted         bool
	cancelled        bool
	cleaning         bool
}

// PrepareTaskWorkspace reserves the final session id and starts only the Git
// worktree work. Provider startup and project post-create commands still wait
// for an explicit Start Task action.
func (m *Manager) PrepareTaskWorkspace(ctx context.Context, project domain.ProjectRecord) (domain.TaskPreparationToken, error) {
	if project.ID == "" {
		return "", nil
	}
	now := m.clock()
	projectID := domain.ProjectID(project.ID)
	rec := seedRecord(ports.SpawnConfig{ProjectID: projectID, Kind: domain.KindWorker}, project.Config, now)
	rec.IsTaskPreparation = true
	rec.ProvisionState = domain.SessionProvisionProvisioning
	rec, err := m.store.CreateSession(ctx, rec)
	if err != nil {
		return "", fmt.Errorf("prepare task workspace: reserve session: %w", err)
	}
	branch := DefaultSpawnBranch(rec.ID, domain.KindWorker, sessionPrefix(project), project.Kind.WithDefault(), m.dataDir)
	rec.Metadata.Branch = branch
	if err := m.store.UpdateSession(ctx, rec); err != nil {
		m.rollbackSpawnSeedRowAfterFailure(ctx, rec.ID)
		return "", fmt.Errorf("prepare task workspace: reserve branch: %w", err)
	}

	prepCtx, cancel := context.WithCancel(m.backgroundContext)
	prep := &taskPreparation{
		record:  rec,
		project: project,
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	token := domain.TaskPreparationToken(rec.ID)
	m.taskPreparationsMu.Lock()
	m.taskPreparations[token] = prep
	m.scheduleTaskPreparationCleanup(token, prep)
	m.taskPreparationsMu.Unlock()

	m.runInBackground(func() { m.createTaskPreparation(prepCtx, prep) })
	return token, nil
}

func (m *Manager) createTaskPreparation(ctx context.Context, prep *taskPreparation) {
	baseRefs := m.refreshDefaultBranchesBestEffort(ctx, prep.project)
	ws, workspaceProject, err := m.createSessionWorkspace(ctx, prep.project, ports.SpawnConfig{
		ProjectID:       domain.ProjectID(prep.project.ID),
		Kind:            domain.KindWorker,
		TaskPreparation: domain.TaskPreparationToken(prep.record.ID),
	}, prep.record.ID, prep.record.Metadata.Branch, baseRefs)
	if err == nil {
		// Keep the original branch tip for cleanup after a daemon restart.
		// Promotion replaces this hidden row's session-facing metadata.
		_, err = m.store.SetTaskPreparationBase(ctx, prep.record.ID, ws.BaseSHA, ws.BaseRef)
	}
	if err == nil {
		var updated bool
		updated, err = m.store.SetSessionProvisionedWorkspace(
			ctx, prep.record.ID, ws.Branch, ws.Path, ws.RepoPath, m.clock(),
		)
		if err == nil && !updated {
			err = errors.New("preparation row no longer exists")
		}
	}
	if err != nil && ws.Path != "" {
		cleanupCtx, cancel := spawnRollbackContext(ctx)
		cleanupErr := m.destroyPreparedWorkspace(cleanupCtx, ws, workspaceProject)
		if cleanupErr == nil {
			cleanupErr = m.deletePreparedBranches(cleanupCtx, ws, workspaceProject)
		}
		cancel()
		if cleanupErr == nil {
			ws = ports.WorkspaceInfo{}
			workspaceProject = nil
		} else {
			err = errors.Join(err, cleanupErr)
		}
	}
	prep.workspace = ws
	prep.workspaceProject = workspaceProject
	prep.err = err
	close(prep.done)
}

// claimTaskPreparation atomically transfers cleanup ownership to Spawn. An
// absent, expired, or wrong-project token is only a cache miss: Spawn falls
// back to creating its own worktree.
func (m *Manager) claimTaskPreparation(token domain.TaskPreparationToken, projectID domain.ProjectID) *taskPreparation {
	token = domain.TaskPreparationToken(strings.TrimSpace(string(token)))
	if token == "" {
		return nil
	}
	m.taskPreparationsMu.Lock()
	defer m.taskPreparationsMu.Unlock()
	prep := m.taskPreparations[token]
	if prep == nil || prep.cancelled || prep.cleaning || domain.ProjectID(prep.project.ID) != projectID {
		return nil
	}
	delete(m.taskPreparations, token)
	prep.timer.Stop()
	return prep
}

func (m *Manager) promoteTaskPreparation(ctx context.Context, prep *taskPreparation, rec domain.SessionRecord) (domain.SessionRecord, error) {
	updated, err := m.store.PromoteTaskPreparation(ctx, prep.record.ID, rec)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	if !updated {
		return domain.SessionRecord{}, errors.New("task preparation is no longer available")
	}
	prep.promoted = true
	return m.getRecord(ctx, prep.record.ID)
}

func (m *Manager) awaitTaskPreparation(ctx context.Context, prep *taskPreparation) (ports.WorkspaceInfo, *ports.WorkspaceProjectInfo, error) {
	select {
	case <-prep.done:
	case <-ctx.Done():
		return ports.WorkspaceInfo{}, nil, ctx.Err()
	}
	return prep.workspace, prep.workspaceProject, prep.err
}

// CancelTaskPreparation is idempotent. Once Spawn has claimed the token,
// cancellation is deliberately ignored so closing the modal cannot delete the
// newly-visible session's worktree.
func (m *Manager) CancelTaskPreparation(ctx context.Context, token domain.TaskPreparationToken) error {
	token = domain.TaskPreparationToken(strings.TrimSpace(string(token)))
	if token == "" {
		return nil
	}
	m.taskPreparationsMu.Lock()
	prep := m.taskPreparations[token]
	if prep == nil {
		m.taskPreparationsMu.Unlock()
		return nil
	}
	if prep.cleaning {
		m.taskPreparationsMu.Unlock()
		return nil
	}
	prep.cancelled = true
	prep.cleaning = true
	prep.timer.Stop()
	prep.cancel()
	m.taskPreparationsMu.Unlock()
	err := m.cleanupTaskPreparation(ctx, prep)
	m.taskPreparationsMu.Lock()
	prep.cleaning = false
	if err == nil {
		if m.taskPreparations[token] == prep {
			delete(m.taskPreparations, token)
		}
	} else if m.taskPreparations[token] == prep {
		m.scheduleTaskPreparationCleanup(token, prep)
	}
	m.taskPreparationsMu.Unlock()
	return err
}

// scheduleTaskPreparationCleanup arms the normal expiry path. Failed cleanup
// uses the same timer so a transient Git or database error never permanently
// consumes the only cleanup handle.
func (m *Manager) scheduleTaskPreparationCleanup(token domain.TaskPreparationToken, prep *taskPreparation) {
	prep.timer = time.AfterFunc(m.taskPreparationTTL, func() {
		cleanupCtx, cleanupCancel := spawnRollbackContext(m.backgroundContext)
		defer cleanupCancel()
		if err := m.CancelTaskPreparation(cleanupCtx, token); err != nil {
			m.logger.Warn("task preparation expiry cleanup failed", "sessionID", prep.record.ID, "error", err)
		}
	})
}

func (m *Manager) cleanupTaskPreparation(ctx context.Context, prep *taskPreparation) error {
	select {
	case <-prep.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if prep.workspace.Path != "" {
		if err := m.destroyPreparedWorkspace(ctx, prep.workspace, prep.workspaceProject); err != nil {
			return err
		}
	}
	if err := m.deletePreparedBranches(ctx, prep.workspace, prep.workspaceProject); err != nil {
		return err
	}
	prep.workspace = ports.WorkspaceInfo{}
	prep.workspaceProject = nil
	if prep.promoted {
		rec, ok, err := m.store.GetSession(ctx, prep.record.ID)
		if err != nil {
			return err
		}
		if ok {
			rec.Metadata.WorkspacePath = ""
			rec.Metadata.WorkspaceRepoPath = ""
			if err := m.store.UpdateSession(ctx, rec); err != nil {
				return err
			}
			m.rollbackSpawnSeedRow(ctx, prep.record.ID)
		}
	} else {
		if _, err := m.store.DeleteTaskPreparation(ctx, prep.record.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) discardClaimedTaskPreparation(ctx context.Context, prep *taskPreparation) {
	prep.cancel()
	defer m.cleanupSystemPromptDir(prep.record.ID)
	if err := m.cleanupTaskPreparation(ctx, prep); err != nil {
		m.logger.Warn("claimed task preparation cleanup failed", "sessionID", prep.record.ID, "error", err)
	}
}

// CleanupInterruptedTaskPreparations reclaims hidden work from a previous
// daemon run before ordinary session reconciliation can mistake it for a live
// session.
func (m *Manager) CleanupInterruptedTaskPreparations(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("list task preparations: %w", err)
	}
	for _, rec := range recs {
		if !rec.IsTaskPreparation {
			continue
		}
		if err := m.cleanupTaskPreparationRecord(ctx, rec); err != nil {
			m.logger.Warn("interrupted task preparation cleanup failed", "sessionID", rec.ID, "error", err)
		}
	}
	return nil
}

func (m *Manager) cleanupTaskPreparationRecord(ctx context.Context, rec domain.SessionRecord) error {
	ws := workspaceInfo(rec)
	ws.BaseSHA = rec.Metadata.DiffBaseSHA
	if ws.Path != "" {
		if rows, ok, err := m.workspaceProjectRows(ctx, rec); err != nil {
			return err
		} else if ok {
			if _, err := m.destroyWorkspaceProjectRows(ctx, rows); err != nil {
				return err
			}
			if err := m.deletePreparedBranches(ctx, ws, &ports.WorkspaceProjectInfo{Worktrees: rows}); err != nil {
				return err
			}
		} else if err := m.workspace.Destroy(ctx, ws); err != nil {
			return err
		} else if err := m.deletePreparedBranches(ctx, ws, nil); err != nil {
			return err
		}
	} else if err := m.deletePreparedBranches(ctx, ws, nil); err != nil {
		return err
	}
	_, err := m.store.DeleteTaskPreparation(ctx, rec.ID)
	return err
}

func (m *Manager) destroyPreparedWorkspace(ctx context.Context, ws ports.WorkspaceInfo, project *ports.WorkspaceProjectInfo) error {
	if project == nil {
		return m.workspace.Destroy(ctx, ws)
	}
	for i := len(project.Worktrees) - 1; i >= 0; i-- {
		info := workspaceInfoFromRepoInfo(project.Worktrees[i])
		if err := m.workspace.Destroy(ctx, info); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) deletePreparedBranches(ctx context.Context, ws ports.WorkspaceInfo, project *ports.WorkspaceProjectInfo) error {
	cleaner, ok := m.workspace.(ports.WorkspacePreparationBranchCleaner)
	if !ok {
		return nil
	}
	if project == nil {
		return cleaner.DeletePreparedBranch(ctx, ws)
	}
	for _, row := range project.Worktrees {
		info := workspaceInfoFromRepoInfo(row)
		info.BaseSHA = row.BaseSHA
		if err := cleaner.DeletePreparedBranch(ctx, info); err != nil {
			return err
		}
	}
	return nil
}
