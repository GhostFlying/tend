-- Execution resources and task workspaces (plan B6, D30, D33, D34, D36).

-- Hosts are defined in the server configuration and synced at startup.
-- token_hash is the SHA-256 of the host's runner token; the token itself is
-- never stored.
CREATE TABLE hosts (
  host_id     TEXT    NOT NULL PRIMARY KEY,
  os          TEXT    NOT NULL,
  arch        TEXT    NOT NULL,
  labels_json TEXT    NOT NULL,
  -- Logical names of the repositories this host serves.
  repos_json  TEXT    NOT NULL,
  slots       INTEGER NOT NULL CHECK (slots >= 0),
  token_hash  TEXT    NOT NULL,
  -- Tailnet nodes allowed to act as this host.
  nodes_json  TEXT    NOT NULL,
  -- active, or retired once it disappears from the configuration.
  status      TEXT    NOT NULL,
  last_seen_at INTEGER,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
) STRICT;

-- Registered repositories. A repository that disappears from the
-- configuration is retired, not deleted, because specs refer to it.
CREATE TABLE repos (
  repo                 TEXT    NOT NULL PRIMARY KEY,
  remote_url           TEXT    NOT NULL,
  -- Ordered base branch candidates; empty means the runner's heuristics.
  base_candidates_json TEXT    NOT NULL,
  retired              INTEGER NOT NULL DEFAULT 0,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
) STRICT;

-- Named workspace templates, expanded into explicit mounts when a task is
-- created.
CREATE TABLE workspace_templates (
  name           TEXT    NOT NULL PRIMARY KEY,
  workspace_json TEXT    NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT;

-- The workspace name is reserved when the task is created; the workspace
-- itself is created on a host when its first implement run is claimed.
ALTER TABLE tasks ADD COLUMN workspace_name TEXT;
CREATE UNIQUE INDEX tasks_workspace_name ON tasks (workspace_name);

CREATE TABLE workspaces (
  workspace_id TEXT    NOT NULL PRIMARY KEY,
  task_id      TEXT    NOT NULL UNIQUE REFERENCES tasks (task_id),
  name         TEXT    NOT NULL UNIQUE,
  host_id      TEXT    NOT NULL REFERENCES hosts (host_id),
  -- assigned, ready, retained, removed
  status       TEXT    NOT NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;

-- One row per mount, filled in when the runner reports how it resolved the
-- base. The spec keeps the intent; this keeps the fact.
CREATE TABLE workspace_mounts (
  workspace_id      TEXT    NOT NULL REFERENCES workspaces (workspace_id),
  path              TEXT    NOT NULL,
  repo              TEXT    NOT NULL REFERENCES repos (repo),
  base_json         TEXT    NOT NULL,
  resolved_base_sha TEXT    NOT NULL,
  -- Which candidate or explicit base the sha came from.
  resolved_from     TEXT    NOT NULL,
  branch            TEXT    NOT NULL,
  added_at          INTEGER NOT NULL,
  PRIMARY KEY (workspace_id, path)
) STRICT;
