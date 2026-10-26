CREATE TABLE background_batches (
 batch_id TEXT PRIMARY KEY,
 group_key TEXT NOT NULL,
 work_item_id TEXT NOT NULL DEFAULT '',
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 status TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 frozen_at INTEGER NOT NULL DEFAULT 0,
 checkin_json TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX background_pending_group ON background_batches(group_key) WHERE status='pending';
CREATE UNIQUE INDEX background_message ON background_batches(message_id);
CREATE TABLE background_sources (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 batch_id TEXT NOT NULL REFERENCES background_batches(batch_id),
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 kind TEXT NOT NULL,
 rule_id TEXT NOT NULL DEFAULT '',
 occurrence_id TEXT NOT NULL DEFAULT '',
 data_json TEXT NOT NULL,
 cancelled_at INTEGER NOT NULL DEFAULT 0,
 UNIQUE(batch_id,event_id)
) STRICT;
CREATE INDEX background_sources_batch ON background_sources(batch_id,seq);
CREATE INDEX background_sources_occurrence ON background_sources(occurrence_id);
CREATE TABLE background_reads (
 batch_id TEXT NOT NULL REFERENCES background_batches(batch_id),
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 source_seq INTEGER NOT NULL REFERENCES background_sources(seq),
 PRIMARY KEY(batch_id,thread_id,turn_id,source_seq)
) STRICT;
-- Background assistant output is retained until the turn is settled, so a
-- silent check-in never flashes a normal chat reply before removing it.
CREATE TABLE background_replies (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 batch_id TEXT NOT NULL REFERENCES background_batches(batch_id),
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 item_id TEXT NOT NULL,
 body TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT 'message',
 tool_json TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL,
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 published INTEGER NOT NULL DEFAULT 0,
 UNIQUE(batch_id,thread_id,turn_id,item_id)
) STRICT;

-- Rule revisions remain monotonic when wall time moves backwards.
CREATE INDEX followup_occurrences_latest ON followup_occurrences(rule_id,revision DESC);
