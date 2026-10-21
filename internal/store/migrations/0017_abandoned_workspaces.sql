-- Workspaces a task gave up because their host became unavailable (D62).
-- The task's next workspace is laid out on another host; the abandoned one
-- is kept here, since a task has at most one row in workspaces.
CREATE TABLE abandoned_workspaces (
  workspace_id TEXT    NOT NULL PRIMARY KEY,
  task_id      TEXT    NOT NULL REFERENCES tasks (task_id),
  name         TEXT    NOT NULL,
  host_id      TEXT    NOT NULL,
  -- The mounts as recorded: path, repo, branch and resolved base.
  mounts_json  TEXT    NOT NULL,
  created_at   INTEGER NOT NULL,
  abandoned_at INTEGER NOT NULL
) STRICT;
CREATE INDEX abandoned_workspaces_task ON abandoned_workspaces (task_id);
