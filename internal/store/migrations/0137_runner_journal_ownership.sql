-- The original journal is identified by the empty string. Existing runs keep
-- that ownership without a fleet-wide rewrite at installation time.
CREATE TABLE runner_journal_runs (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 journal_id TEXT NOT NULL CHECK(journal_id <> '')
) STRICT;
CREATE TABLE draining_runners (
 host_id TEXT NOT NULL REFERENCES hosts(host_id),
 instance_id TEXT NOT NULL,
 journal_id TEXT NOT NULL,
 upgrade_id TEXT NOT NULL REFERENCES upgrades(upgrade_id),
 runtime_json TEXT NOT NULL,
 control_json TEXT NOT NULL,
 last_heartbeat_at INTEGER NOT NULL,
 PRIMARY KEY(host_id, instance_id),
 UNIQUE(host_id, journal_id)
) STRICT;
CREATE TABLE runner_retirements (
 host_id TEXT NOT NULL REFERENCES hosts(host_id),
 instance_id TEXT NOT NULL,
 upgrade_id TEXT NOT NULL REFERENCES upgrades(upgrade_id),
 journal_id TEXT NOT NULL,
 authorization_id TEXT NOT NULL UNIQUE,
 process_json TEXT NOT NULL,
 confirmed_at INTEGER,
 PRIMARY KEY(host_id,instance_id)
) STRICT;

-- A task can finish or detach its shared workspace after a contributing
-- journal has retired. A later run invalidates the saved count automatically.
CREATE TABLE workspace_journal_releases (
 workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 journal_id TEXT NOT NULL,
 run_count INTEGER NOT NULL CHECK(run_count>0),
 PRIMARY KEY(workspace_id,task_id,journal_id)
) STRICT;
