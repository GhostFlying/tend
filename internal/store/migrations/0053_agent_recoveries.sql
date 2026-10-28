ALTER TABLE main_turns ADD COLUMN failure_json TEXT NOT NULL DEFAULT '';

CREATE TABLE agent_recoveries (
 root_request_id TEXT PRIMARY KEY REFERENCES main_turns(request_id),
 conversation_id TEXT NOT NULL,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 thread_id TEXT NOT NULL,
 status TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL DEFAULT 0,
 next_reconcile_at INTEGER NOT NULL DEFAULT 0,
 deadline INTEGER NOT NULL,
 active_request_id TEXT NOT NULL,
 last_turn_id TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 successor_message_id TEXT NOT NULL DEFAULT '',
 notice_message_id TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL
);
CREATE TABLE agent_recovery_attempts (
 request_id TEXT PRIMARY KEY,
 root_request_id TEXT NOT NULL REFERENCES agent_recoveries(root_request_id),
 source_request_id TEXT NOT NULL REFERENCES main_turns(request_id),
 UNIQUE(source_request_id)
);
CREATE INDEX agent_recovery_message ON agent_recoveries(message_id);

CREATE TABLE agent_recovery_stops (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 stopped_at INTEGER NOT NULL
);
