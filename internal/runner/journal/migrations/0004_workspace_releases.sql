CREATE TABLE workspace_releases (
  workspace_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
) STRICT;
