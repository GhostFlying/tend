-- Workspace resources survive individual task bindings and can be reused serially.
ALTER TABLE hosts ADD COLUMN workspace_protocol INTEGER NOT NULL DEFAULT 0;

CREATE TABLE workspace_mounts_saved AS SELECT * FROM workspace_mounts;
CREATE TABLE workspaces_saved AS SELECT * FROM workspaces;
DROP TABLE workspace_mounts;
DROP TABLE workspaces;

CREATE TABLE workspaces (
  workspace_id TEXT NOT NULL PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(task_id),
  name TEXT NOT NULL,
  host_id TEXT NOT NULL REFERENCES hosts(host_id),
  status TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'git' CHECK (kind IN ('git', 'directory')),
  occupied_by TEXT REFERENCES tasks(task_id),
  provider_id TEXT NOT NULL DEFAULT '',
  provider_instance_id TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  released_at INTEGER,
  cleanup_id TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX workspace_host_status ON workspaces(host_id, status);

INSERT INTO workspaces(workspace_id,task_id,name,host_id,status,occupied_by,created_at,updated_at)
SELECT w.workspace_id,w.task_id,w.name,w.host_id,w.status,
  -- Legacy successful workers may have left their native TUI alive.
  -- Only runner reconciliation of every historical run can release it.
  w.task_id,
  w.created_at,w.updated_at
FROM workspaces_saved w JOIN tasks t ON t.task_id=w.task_id;

CREATE TABLE task_workspace_bindings (
  task_id TEXT NOT NULL PRIMARY KEY REFERENCES tasks(task_id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
  mode TEXT NOT NULL CHECK(mode IN ('new','reuse','temporary')),
  source_task_id TEXT REFERENCES tasks(task_id),
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX workspace_task_bindings ON task_workspace_bindings(workspace_id);
INSERT INTO task_workspace_bindings(task_id,workspace_id,mode,created_at)
SELECT task_id,workspace_id,'new',created_at FROM workspaces;

CREATE TABLE workspace_runs (
  run_id TEXT NOT NULL PRIMARY KEY REFERENCES runs(run_id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id)
) STRICT;
CREATE INDEX workspace_run_resources ON workspace_runs(workspace_id);
INSERT INTO workspace_runs(run_id,workspace_id)
SELECT r.run_id,w.workspace_id FROM runs r JOIN workspaces w ON w.task_id=r.task_id AND w.host_id=r.host_id;

CREATE TABLE workspace_mounts (
  workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
  path TEXT NOT NULL,
  repo TEXT NOT NULL REFERENCES repos(repo),
  base_json TEXT NOT NULL,
  resolved_base_sha TEXT NOT NULL,
  resolved_from TEXT NOT NULL,
  branch TEXT NOT NULL,
  added_at INTEGER NOT NULL,
  PRIMARY KEY(workspace_id,path)
) STRICT;
INSERT INTO workspace_mounts SELECT * FROM workspace_mounts_saved;
DROP TABLE workspace_mounts_saved;
DROP TABLE workspaces_saved;

CREATE TABLE workspace_baselines (
  workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
  task_id TEXT NOT NULL REFERENCES tasks(task_id),
  body_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(workspace_id,task_id)
) STRICT;
