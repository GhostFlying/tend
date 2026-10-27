-- A run-scoped outbound queue, with immutable source and delivery history.
-- Existing sessions did not receive the new tools; a later heartbeat alone
-- cannot upgrade their original assignment's reception capability.
ALTER TABLE runs ADD COLUMN run_messages_enabled INTEGER NOT NULL DEFAULT 0 CHECK(run_messages_enabled IN (0,1));
CREATE TABLE run_messages (
 message_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 action_id TEXT NOT NULL REFERENCES plan_actions(action_id),
 spec_version INTEGER NOT NULL,
 generation INTEGER NOT NULL,
 control_revision INTEGER NOT NULL,
 sequence INTEGER NOT NULL,
 text TEXT NOT NULL,
 source_json TEXT NOT NULL,
 source_message_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','dispatching','accepted','failed','unknown','unsupported')),
 reason TEXT NOT NULL DEFAULT '',
 receiver TEXT NOT NULL DEFAULT '',
 thread_id TEXT NOT NULL DEFAULT '',
 turn_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(run_id,sequence)
) STRICT;
CREATE INDEX run_messages_pending ON run_messages(run_id,sequence) WHERE status IN ('queued','dispatching','unknown');
CREATE TABLE run_message_history (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 message_id TEXT NOT NULL REFERENCES run_messages(message_id),
 status TEXT NOT NULL,
 reason TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
