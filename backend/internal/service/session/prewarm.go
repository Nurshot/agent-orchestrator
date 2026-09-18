package session

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// spawnPrewarmer is an optional Session Manager capability. Keeping it off the
// commander interface leaves focused service fakes untouched: a build without
// it simply does the refresh inside the spawn, as before.
type spawnPrewarmer interface {
	PrewarmSpawn(ctx context.Context, projectID domain.ProjectID) error
}

// PrewarmSpawn warms the work a spawn would otherwise do with the user
// watching — today, the default-branch refresh. The desktop calls it when the
// new-task dialog opens.
//
// It returns as soon as the work is scheduled. There is deliberately nothing to
// wait for and nothing to report: a spawn whose prewarm never finished behaves
// exactly as it did before this existed.
func (s *Service) PrewarmSpawn(ctx context.Context, projectID domain.ProjectID) error {
	if _, err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	prewarmer, ok := s.manager.(spawnPrewarmer)
	if !ok {
		return nil
	}
	return prewarmer.PrewarmSpawn(ctx, projectID)
}
