-- Steer attempts retain the original outbox identity and exact native input.
CREATE TABLE main_thread_toolsets (
 instance TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 toolset_hash TEXT NOT NULL,
 PRIMARY KEY(instance,thread_id)
) STRICT;
CREATE TABLE main_steers (
 request_id TEXT PRIMARY KEY REFERENCES outbox(request_id),
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 input_text TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('sending','accepted','consumed','completed','interrupted','failed','unknown','waiting')),
 native_item_id TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX main_steers_turn ON main_steers(thread_id,turn_id,status);
CREATE TABLE main_tool_sources (
 native_key TEXT PRIMARY KEY,
 message_id TEXT NOT NULL,
 request_id TEXT NOT NULL
) STRICT;
CREATE TABLE conversation_reply_sources (
 reply_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 source_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 PRIMARY KEY(reply_id,source_id)
) STRICT;
CREATE TRIGGER conversation_block_steer AFTER INSERT ON events
 WHEN NEW.kind='conversation.message_state' AND json_extract(NEW.body_json,'$.state')='accepted'
 BEGIN INSERT OR IGNORE INTO conversation_blocks(message_id) VALUES(NEW.object_id); END;
