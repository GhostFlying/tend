CREATE TABLE concurrency_groups (
 group_id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 state_json TEXT NOT NULL,
 deleted INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE concurrency_bindings (
 environment_id TEXT NOT NULL,
 runtime_id TEXT NOT NULL,
 group_id TEXT NOT NULL REFERENCES concurrency_groups(group_id),
 PRIMARY KEY(environment_id,runtime_id)
) STRICT;
CREATE INDEX concurrency_bindings_group ON concurrency_bindings(group_id);
CREATE TABLE concurrency_executions (
 execution_id TEXT PRIMARY KEY,
 environment_id TEXT NOT NULL,
 runtime_id TEXT NOT NULL,
 state TEXT NOT NULL,
 source_json TEXT NOT NULL,
 last_seen INTEGER NOT NULL
) STRICT;
CREATE INDEX concurrency_execution_runtime ON concurrency_executions(environment_id,runtime_id,state);
CREATE TABLE concurrency_changes (
 change_id INTEGER PRIMARY KEY AUTOINCREMENT,
 group_id TEXT NOT NULL REFERENCES concurrency_groups(group_id),
 at INTEGER NOT NULL,
 reason TEXT NOT NULL,
 snapshot_json TEXT NOT NULL
) STRICT;
CREATE INDEX concurrency_changes_history ON concurrency_changes(at DESC,change_id DESC);
CREATE TABLE concurrency_feedback (
 feedback_id TEXT PRIMARY KEY,
 group_id TEXT NOT NULL,
 change_id INTEGER NOT NULL DEFAULT 0,
 received_at INTEGER NOT NULL,
 source_json TEXT NOT NULL
) STRICT;
CREATE INDEX concurrency_feedback_change ON concurrency_feedback(change_id);
CREATE TABLE concurrency_waiters (
 request_id TEXT PRIMARY KEY,
 environment_id TEXT NOT NULL,
 runtime_id TEXT NOT NULL,
 last_seen INTEGER NOT NULL
) STRICT;
ALTER TABLE concurrency_executions ADD COLUMN group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE concurrency_executions ADD COLUMN parent_execution_id TEXT NOT NULL DEFAULT '';
CREATE INDEX concurrency_execution_parent ON concurrency_executions(parent_execution_id,state);
