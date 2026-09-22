-- Additional repositories a session's worker clones alongside the project's
-- primary repo (multi-repo dev kit for the coder template flow). Nullable and
-- defaulted to an empty array, so every existing single-repo session is
-- unaffected. Stored as jsonb array of {"url","branch"} objects.
-- +goose Up
ALTER TABLE ao_sessions
    ADD COLUMN extra_repos jsonb NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE ao_sessions
    DROP COLUMN extra_repos;
