-- Retain the runner-reported physical worktree independently of its logical key.
-- Historical layouts cannot be inferred by joining workspace path and mount key.
ALTER TABLE workspace_mounts ADD COLUMN directory TEXT NOT NULL DEFAULT '';
