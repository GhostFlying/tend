-- Hosted native history is retained while front-thread bindings advance.
CREATE TABLE conversation_handoff_settings (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 idle_minutes INTEGER NOT NULL CHECK(idle_minutes>=0),
 revision INTEGER NOT NULL CHECK(revision>0),
 updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE conversation_coverage (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 version INTEGER NOT NULL,
 last_activity INTEGER NOT NULL
) STRICT;
INSERT INTO conversation_coverage VALUES(1,0,0);
CREATE TABLE main_thread_bindings (
 instance TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 toolset_hash TEXT NOT NULL,
 path TEXT NOT NULL,
 generation INTEGER NOT NULL,
 ancestors_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(instance,thread_id)
) STRICT;
CREATE TABLE conversation_summaries (
 summary_id TEXT PRIMARY KEY,
 instance TEXT NOT NULL,
 source_thread_id TEXT NOT NULL,
 source_version INTEGER NOT NULL,
 snapshot_json TEXT NOT NULL,
 body TEXT NOT NULL,
 format_version INTEGER NOT NULL CHECK(format_version>0),
 created_at INTEGER NOT NULL,
 UNIQUE(instance,source_thread_id,source_version,format_version)
) STRICT;
CREATE TABLE main_thread_handoffs (
 handoff_id TEXT PRIMARY KEY,
 instance TEXT NOT NULL,
 source_thread_id TEXT NOT NULL,
 source_generation INTEGER NOT NULL,
 source_version INTEGER NOT NULL,
 toolset_hash TEXT NOT NULL,
 target_thread_id TEXT NOT NULL DEFAULT '',
 target_path TEXT NOT NULL DEFAULT '',
 prompt TEXT NOT NULL,
 phase TEXT NOT NULL CHECK(phase IN ('prepared','creating','created','injecting','injected','committed','unknown')),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX main_thread_handoff_pending ON main_thread_handoffs(instance)
 WHERE phase<>'committed';
-- Latest message changes, including deletions, support complete summary deltas.
CREATE TABLE conversation_changes (
 message_id TEXT PRIMARY KEY,
 last_version INTEGER NOT NULL
) STRICT;
INSERT INTO conversation_changes SELECT message_id,(SELECT version FROM conversation_coverage WHERE singleton=1) FROM conversation_messages;
CREATE TRIGGER coverage_conversation_messages_insert AFTER INSERT ON conversation_messages
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_messages_update AFTER UPDATE ON conversation_messages WHEN OLD.author IS NOT NEW.author OR OLD.client_id IS NOT NEW.client_id OR OLD.body IS NOT NEW.body OR OLD.reply_to IS NOT NEW.reply_to OR OLD.last_seen_seq IS NOT NEW.last_seen_seq OR OLD.request_id IS NOT NEW.request_id OR OLD.kind IS NOT NEW.kind OR OLD.decision_json IS NOT NEW.decision_json
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_messages_delete AFTER DELETE ON conversation_messages
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_tool_uses_insert AFTER INSERT ON conversation_tool_uses
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_tool_uses_update AFTER UPDATE ON conversation_tool_uses WHEN OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.item_id IS NOT NEW.item_id OR OLD.tool IS NOT NEW.tool OR OLD.input IS NOT NEW.input OR OLD.output IS NOT NEW.output OR OLD.status IS NOT NEW.status
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_tool_uses_delete AFTER DELETE ON conversation_tool_uses
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_agent_messages_insert AFTER INSERT ON conversation_agent_messages
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_agent_messages_update AFTER UPDATE ON conversation_agent_messages WHEN OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.item_id IS NOT NEW.item_id OR OLD.status IS NOT NEW.status OR OLD.revision IS NOT NEW.revision
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_agent_messages_delete AFTER DELETE ON conversation_agent_messages
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_main_turns_insert AFTER INSERT ON main_turns
 BEGIN UPDATE conversation_coverage SET version=version+1,last_activity=MAX(last_activity,NEW.updated_at) WHERE singleton=1; END;
CREATE TRIGGER coverage_main_turns_update AFTER UPDATE ON main_turns WHEN OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.status IS NOT NEW.status OR OLD.model IS NOT NEW.model OR OLD.model_provider IS NOT NEW.model_provider OR OLD.effort IS NOT NEW.effort OR OLD.settings_revision IS NOT NEW.settings_revision
 BEGIN UPDATE conversation_coverage SET version=version+1,last_activity=MAX(last_activity,NEW.updated_at) WHERE singleton=1; END;
CREATE TRIGGER coverage_main_turns_delete AFTER DELETE ON main_turns
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; END;
-- Conversation events supply the injected application clock, including streaming
-- body updates and message state transitions. Summary bookkeeping is excluded.
UPDATE conversation_coverage SET last_activity=MAX(
 COALESCE((SELECT MAX(created_at) FROM events WHERE kind IN ('conversation.message','conversation.message_state','conversation.tool_use')),0),
 COALESCE((SELECT MAX(updated_at) FROM main_turns),0));
CREATE TRIGGER coverage_conversation_event AFTER INSERT ON events
 WHEN NEW.kind IN ('conversation.message','conversation.message_state','conversation.tool_use')
 BEGIN UPDATE conversation_coverage SET version=version+1,last_activity=MAX(last_activity,NEW.created_at) WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.object_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TABLE conversation_summary_attempts (
 instance TEXT NOT NULL,
 source_thread_id TEXT NOT NULL,
 source_version INTEGER NOT NULL,
 binding_generation INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('started','failed','succeeded')),
 retry_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(instance,source_thread_id,source_version,binding_generation)
) STRICT;
