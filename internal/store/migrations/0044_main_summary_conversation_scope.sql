-- D111: main summary coverage must ignore background-only activity.
-- Keep the existing monotonic version for historical summary compatibility.

DROP TRIGGER coverage_conversation_messages_insert;
CREATE TRIGGER coverage_conversation_messages_insert AFTER INSERT ON conversation_messages WHEN COALESCE((SELECT conversation_id FROM conversation_messages WHERE message_id=NEW.reply_to AND NEW.author='main_agent'),NEW.conversation_id)='main'
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_messages_update;
CREATE TRIGGER coverage_conversation_messages_update AFTER UPDATE ON conversation_messages WHEN (OLD.author IS NOT NEW.author OR OLD.client_id IS NOT NEW.client_id OR OLD.body IS NOT NEW.body OR OLD.reply_to IS NOT NEW.reply_to OR OLD.last_seen_seq IS NOT NEW.last_seen_seq OR OLD.request_id IS NOT NEW.request_id OR OLD.kind IS NOT NEW.kind OR OLD.decision_json IS NOT NEW.decision_json) AND COALESCE((SELECT conversation_id FROM conversation_messages WHERE message_id=NEW.reply_to AND NEW.author='main_agent'),NEW.conversation_id)='main'
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_messages_delete;
CREATE TRIGGER coverage_conversation_messages_delete AFTER DELETE ON conversation_messages WHEN COALESCE((SELECT conversation_id FROM conversation_messages WHERE message_id=OLD.reply_to AND OLD.author='main_agent'),OLD.conversation_id)='main'
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_tool_uses_insert;
CREATE TRIGGER coverage_conversation_tool_uses_insert AFTER INSERT ON conversation_tool_uses WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_tool_uses_update;
CREATE TRIGGER coverage_conversation_tool_uses_update AFTER UPDATE ON conversation_tool_uses WHEN (OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.item_id IS NOT NEW.item_id OR OLD.tool IS NOT NEW.tool OR OLD.input IS NOT NEW.input OR OLD.output IS NOT NEW.output OR OLD.status IS NOT NEW.status) AND EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_tool_uses_delete;
CREATE TRIGGER coverage_conversation_tool_uses_delete AFTER DELETE ON conversation_tool_uses WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=OLD.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_agent_messages_insert;
CREATE TRIGGER coverage_conversation_agent_messages_insert AFTER INSERT ON conversation_agent_messages WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_agent_messages_update;
CREATE TRIGGER coverage_conversation_agent_messages_update AFTER UPDATE ON conversation_agent_messages WHEN (OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.item_id IS NOT NEW.item_id OR OLD.status IS NOT NEW.status OR OLD.revision IS NOT NEW.revision) AND EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_agent_messages_delete;
CREATE TRIGGER coverage_conversation_agent_messages_delete AFTER DELETE ON conversation_agent_messages WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=OLD.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_main_turns_insert;
CREATE TRIGGER coverage_main_turns_insert AFTER INSERT ON main_turns WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1,last_activity=MAX(last_activity,NEW.updated_at) WHERE singleton=1; END;

DROP TRIGGER coverage_main_turns_update;
CREATE TRIGGER coverage_main_turns_update AFTER UPDATE ON main_turns WHEN (OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.status IS NOT NEW.status OR OLD.model IS NOT NEW.model OR OLD.model_provider IS NOT NEW.model_provider OR OLD.effort IS NOT NEW.effort OR OLD.settings_revision IS NOT NEW.settings_revision) AND EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1,last_activity=MAX(last_activity,NEW.updated_at) WHERE singleton=1; END;

DROP TRIGGER coverage_main_turns_delete;
CREATE TRIGGER coverage_main_turns_delete AFTER DELETE ON main_turns WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=OLD.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; END;

DROP TRIGGER coverage_conversation_event;
CREATE TRIGGER coverage_conversation_event AFTER INSERT ON events
 WHEN (NEW.kind IN ('conversation.message','conversation.message_state','conversation.tool_use')) AND EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.object_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1,last_activity=MAX(last_activity,NEW.created_at) WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.object_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_images_insert;
CREATE TRIGGER coverage_conversation_images_insert AFTER INSERT ON conversation_images WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_images_update;
CREATE TRIGGER coverage_conversation_images_update AFTER UPDATE ON conversation_images WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_images_delete;
CREATE TRIGGER coverage_conversation_images_delete AFTER DELETE ON conversation_images WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=OLD.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_message_attachments_insert;
CREATE TRIGGER coverage_conversation_message_attachments_insert AFTER INSERT ON conversation_message_attachments WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_message_attachments_delete;
CREATE TRIGGER coverage_conversation_message_attachments_delete AFTER DELETE ON conversation_message_attachments WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=OLD.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER message_control_coverage;
CREATE TRIGGER message_control_coverage AFTER UPDATE ON conversation_message_controls WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN
 UPDATE conversation_coverage SET version=version+1 WHERE singleton=1;
 INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version;
 END;

DROP TRIGGER coverage_association_insert;
CREATE TRIGGER coverage_association_insert AFTER INSERT ON message_work_items WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN
 UPDATE conversation_coverage SET version=version+1 WHERE singleton=1;
 INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version;
END;

DROP TRIGGER coverage_association_update;
CREATE TRIGGER coverage_association_update AFTER UPDATE ON message_work_items WHEN EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN
 UPDATE conversation_coverage SET version=version+1 WHERE singleton=1;
 INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version;
END;

-- Background activity from a previously opened database is not main idle time.
UPDATE conversation_coverage SET version=version+1,last_activity=MAX(
 COALESCE((SELECT MAX(e.created_at) FROM events e JOIN conversation_messages m ON m.message_id=e.object_id WHERE m.conversation_id='main' AND e.kind IN ('conversation.message','conversation.message_state','conversation.tool_use')),0),
 COALESCE((SELECT MAX(t.updated_at) FROM main_turns t JOIN conversation_messages m ON m.message_id=t.message_id WHERE m.conversation_id='main'),0)
) WHERE singleton=1;
