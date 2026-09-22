package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Asynchronous Chat spawn.
//
// A synchronous spawn holds the API open for everything a session needs: a
// remote fetch, a worktree checkout, and the provider's own session/new. On a
// real repository that is seconds of staring at a spinner before the session
// the user just described is even visible.
//
// This path answers as soon as the two things the user interacts with exist —
// the session row and its conversation — and finishes the rest in the
// background. The opening prompt is recorded as a queued turn rather than sent,
// which is also what makes the in-between typeable: everything the user writes
// before the controller arrives lands in the same durable queue, in order, and
// the controller drains it when it starts.
//
// Only worker sessions take this path. An orchestrator owns a project-scoped
// narrative whose rebinding must stay ordered with its controller.

// asyncChatSpawn is the resolved spawn state handed to the background half.
type asyncChatSpawn struct {
	cfg               ports.SpawnConfig
	project           domain.ProjectRecord
	projectKind       domain.ProjectKind
	record            domain.SessionRecord
	branch            string
	prompt            string
	systemPrompt      string
	promptBytes       int
	systemPromptBytes int
	preparation       *taskPreparation
}

// beginAsyncChatSpawn publishes the session, records the opening prompt in the
// durable queue, and hands the rest to the background.
func (m *Manager) beginAsyncChatSpawn(ctx context.Context, in asyncChatSpawn) (domain.SessionRecord, int, int, error) {
	id := in.record.ID
	rollback := func() {
		if in.preparation == nil {
			m.rollbackSpawnSeedRowAfterFailure(ctx, id)
			return
		}
		cleanupCtx, cancel := spawnRollbackContext(ctx)
		m.discardClaimedTaskPreparation(cleanupCtx, in.preparation)
		cancel()
	}
	rec, err := m.setProvisionState(ctx, id, domain.SessionProvisionProvisioning, "")
	if err != nil {
		rollback()
		return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnCreate, err)
	}
	if in.prompt != "" {
		if _, err := m.chat.QueueChatPrompt(ctx, id, in.prompt); err != nil {
			rollback()
			return domain.SessionRecord{}, 0, 0, wrapSpawnStage(id, ErrSpawnDeliverPrompt, err)
		}
		rec, err = m.getRecord(ctx, id)
		if err != nil {
			rollback()
			return domain.SessionRecord{}, 0, 0, err
		}
	}
	in.record = rec
	m.runInBackground(func() {
		// The HTTP request that started this is already answered; its context is
		// gone. The work continues under the daemon's lifetime instead.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncChatSpawnBudget)
		defer cancel()
		m.completeAsyncChatSpawn(bg, in)
	})
	return rec, in.promptBytes, in.systemPromptBytes, nil
}

// asyncChatSpawnBudget bounds a background start so a hung provider leaves a
// failed session the user can act on rather than a permanent "starting".
const asyncChatSpawnBudget = 10 * time.Minute

// completeAsyncChatSpawn builds everything the early answer skipped. Failures
// mark the session failed instead of deleting it: the user is already looking
// at the session, and their queued messages live in it.
func (m *Manager) completeAsyncChatSpawn(ctx context.Context, in asyncChatSpawn) {
	id := in.record.ID
	totalStarted := time.Now()
	stageStarted := totalStarted
	var ws ports.WorkspaceInfo
	var workspaceProject *ports.WorkspaceProjectInfo
	var err error
	if in.preparation != nil {
		ws, workspaceProject, err = m.awaitTaskPreparation(ctx, in.preparation)
		if err != nil && ctx.Err() != nil {
			in.preparation.cancel()
			m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceCreate, err))
			return
		}
	}
	if ws.Path == "" {
		baseRefs := m.refreshDefaultBranchesBestEffort(ctx, in.project)
		m.logAsyncChatSpawnStage(id, "default_branch_refresh", stageStarted)
		stageStarted = time.Now()
		ws, workspaceProject, err = m.createSessionWorkspace(ctx, in.project, in.cfg, id, in.branch, baseRefs)
	} else {
		m.logAsyncChatSpawnStage(id, "prepared_workspace_wait", stageStarted)
		stageStarted = time.Now()
	}
	if err != nil {
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceCreate, err))
		return
	}
	m.logAsyncChatSpawnStage(id, "workspace_create", stageStarted)
	stageStarted = time.Now()
	if err := m.provisionWorkspace(ctx, in.project, ws.Path); err != nil {
		if m.destroySpawnWorkspace(ctx, ws, workspaceProject) {
			m.clearProvisionedWorkspace(ctx, id, ws.Path)
		}
		m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrWorkspaceProvision, err))
		return
	}
	m.logAsyncChatSpawnStage(id, "workspace_provision", stageStarted)
	if len(in.cfg.Attachments) > 0 {
		stageStarted = time.Now()
		// The prompt already references these by name (spawnAttachmentRefs); this
		// is where the bytes land, before the agent can read them.
		if _, err := m.writeSpawnAttachments(ctx, id, ws.Path, in.cfg.Attachments); err != nil {
			if m.destroySpawnWorkspace(ctx, ws, workspaceProject) {
				m.clearProvisionedWorkspace(ctx, id, ws.Path)
			}
			m.failAsyncChatSpawn(ctx, id, wrapSpawnStage(id, ErrSpawnAttachments, err))
			return
		}
		if err := m.workspace.AddExclude(ctx, ws, "/"+attachmentsDir+"/"); err != nil {
			m.logger.Warn("spawn: exclude attachments dir", "sessionID", id, "error", err)
		}
		m.logAsyncChatSpawnStage(id, "spawn_attachments", stageStarted)
	}
	// Anything the user attached while this was starting was written canonically
	// only, because there was no worktree to put it in. Replay it now, before the
	// controller can read the turn that references those paths.
	stageStarted = time.Now()
	if err := m.restoreAttachments(ctx, id, ws); err != nil {
		m.logger.Warn("spawn: materialize attachments staged while provisioning",
			"sessionID", id, "error", err)
	}
	m.logAsyncChatSpawnStage(id, "attachment_restore", stageStarted)

	// Publish the worktree now rather than at the controller commit. Until the
	// row carries it, every workspace-scoped read answers
	// SESSION_WORKSPACE_NOT_FOUND, and the provider start that follows is long
	// enough for the desktop's bounded readiness poll to give up on a session
	// that is perfectly fine. It also means an interrupted start leaves a row
	// that knows which worktree to clean up.
	stageStarted = time.Now()
	if _, err := m.store.SetSessionProvisionedWorkspace(
		ctx, id, ws.Branch, ws.Path, ws.RepoPath, m.clock()); err != nil {
		m.logger.Warn("spawn: publish provisioned workspace", "sessionID", id, "error", err)
	}
	m.logAsyncChatSpawnStage(id, "workspace_publish", stageStarted)

	record, err := m.getRecord(ctx, id)
	if err != nil {
		if m.destroySpawnWorkspace(ctx, ws, workspaceProject) {
			m.clearProvisionedWorkspace(ctx, id, ws.Path)
		}
		m.failAsyncChatSpawn(ctx, id, err)
		return
	}
	stageStarted = time.Now()
	if _, err := m.launchChatController(ctx, chatSpawn{
		cfg:              in.cfg,
		project:          in.project,
		projectKind:      in.projectKind,
		record:           record,
		workspace:        ws,
		workspaceProject: workspaceProject,
		prompt:           in.prompt,
		systemPrompt:     in.systemPrompt,
		// The opening prompt is already a queued turn; the drain below delivers
		// it together with anything typed while this was starting.
		promptQueued: true,
	}); err != nil {
		m.failAsyncChatSpawn(ctx, id, err)
		return
	}
	m.logAsyncChatSpawnStage(id, "controller_start", stageStarted)
	stageStarted = time.Now()
	if err := m.chat.DrainChatQueue(ctx, id); err != nil {
		m.logger.Error("spawn: dispatch queued prompt", "sessionID", id, "error", err)
	}
	m.logAsyncChatSpawnStage(id, "queue_drain", stageStarted)
	stageStarted = time.Now()
	if _, err := m.setProvisionState(ctx, id, domain.SessionProvisionReady, ""); err != nil {
		m.logger.Error("spawn: publish provisioned session", "sessionID", id, "error", err)
	}
	m.logAsyncChatSpawnStage(id, "mark_ready", stageStarted)
	m.logAsyncChatSpawnStage(id, "total", totalStarted)
}

func (m *Manager) logAsyncChatSpawnStage(id domain.SessionID, stage string, started time.Time) {
	m.logger.Info("spawn: asynchronous chat stage",
		"sessionID", id,
		"stage", stage,
		"duration", time.Since(started),
	)
}

// failAsyncChatSpawn records why a background start stopped. The row, its
// conversation, and its queue survive so the failure is something the user can
// read and retry rather than a session that silently disappeared.
func (m *Manager) failAsyncChatSpawn(ctx context.Context, id domain.SessionID, cause error) {
	m.logger.Error("spawn: asynchronous chat start failed", "sessionID", id, "error", cause)
	cleanupCtx, cancel := spawnRollbackContext(ctx)
	defer cancel()
	m.stopChatBestEffort(cleanupCtx, id)
	if _, err := m.setProvisionState(cleanupCtx, id, domain.SessionProvisionFailed, cause.Error()); err != nil {
		m.logger.Error("spawn: record failed start", "sessionID", id, "error", err)
	}
}

func (m *Manager) setProvisionState(
	ctx context.Context,
	id domain.SessionID,
	state domain.SessionProvisionState,
	message string,
) (domain.SessionRecord, error) {
	if _, err := m.store.SetSessionProvisionState(ctx, id, state, message, m.clock()); err != nil {
		return domain.SessionRecord{}, err
	}
	return m.getRecord(ctx, id)
}

// FailInterruptedProvisioning marks sessions whose background start did not
// survive a daemon restart. Without this a row left mid-start reads as
// "starting" forever: nothing is running that could ever finish it.
func (m *Manager) FailInterruptedProvisioning(ctx context.Context) error {
	recs, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("list sessions for interrupted starts: %w", err)
	}
	var failures []error
	for _, rec := range recs {
		if rec.IsTerminated || !rec.ProvisionState.IsProvisioning() {
			continue
		}
		if _, err := m.setProvisionState(ctx, rec.ID, domain.SessionProvisionFailed,
			"AO restarted before this session finished starting"); err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", rec.ID, err))
		}
	}
	return errors.Join(failures...)
}

// runInBackground runs work outside the caller's request. The seam exists so
// tests can observe a completed spawn without sleeping.
func (m *Manager) runInBackground(work func()) {
	if m.runBackground != nil {
		m.runBackground(work)
		return
	}
	go work()
}

func (m *Manager) clearProvisionedWorkspace(ctx context.Context, id domain.SessionID, workspacePath string) {
	cleanupCtx, cancel := spawnRollbackContext(ctx)
	defer cancel()
	rec, ok, err := m.store.GetSession(cleanupCtx, id)
	if err != nil || !ok || rec.Metadata.WorkspacePath != workspacePath {
		return
	}
	rec.Metadata.Branch = ""
	rec.Metadata.WorkspacePath = ""
	rec.Metadata.WorkspaceRepoPath = ""
	if err := m.store.UpdateSession(cleanupCtx, rec); err != nil {
		m.logger.Warn("spawn: clear removed workspace", "sessionID", id, "error", err)
	}
}
