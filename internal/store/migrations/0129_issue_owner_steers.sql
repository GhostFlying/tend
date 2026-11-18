CREATE TABLE issue_owner_steers (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 issue_revision INTEGER NOT NULL,
 assignment_epoch INTEGER NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 execution_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 source_message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 source_revision INTEGER NOT NULL,
 revokes_source_message_id TEXT REFERENCES conversation_messages(message_id),
 sender_json TEXT NOT NULL,
 input_header TEXT NOT NULL,
 input_sha256 TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','sending','accepted','consumed','unsupported','rejected','unknown')),
 reason TEXT NOT NULL DEFAULT '',
 accepted_at INTEGER NOT NULL DEFAULT 0,
 native_request_id TEXT UNIQUE,
 native_item_id TEXT NOT NULL DEFAULT '',
 UNIQUE(issue_id,source_message_id)
) STRICT;
CREATE INDEX issue_owner_steers_active ON issue_owner_steers(execution_id,status);
CREATE INDEX issue_owner_steers_receipt ON issue_owner_steers(thread_id,turn_id,status);
CREATE INDEX issue_owner_steers_revoked ON issue_owner_steers(issue_id,revokes_source_message_id)
 WHERE revokes_source_message_id IS NOT NULL;
CREATE INDEX issue_owner_steers_run ON issue_owner_steers(run_id,status);
