-- Message controls are independent of immutable message content and run state.
CREATE TABLE conversation_message_controls (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id) ON DELETE CASCADE,
 revision INTEGER NOT NULL DEFAULT 1,
 withdrawn_at INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO conversation_message_controls(message_id)
 SELECT message_id FROM conversation_messages WHERE author='owner';
CREATE TRIGGER message_control_insert AFTER INSERT ON conversation_messages WHEN NEW.author='owner'
 BEGIN INSERT INTO conversation_message_controls(message_id) VALUES(NEW.message_id); END;
CREATE TRIGGER message_control_delivery AFTER UPDATE OF status ON outbox
 WHEN NEW.destination='main_thread' AND NEW.operation='main_thread.handle_message' AND OLD.status<>NEW.status
 BEGIN UPDATE conversation_message_controls SET revision=revision+1 WHERE message_id=json_extract(NEW.payload_json,'$.message_id'); END;
CREATE TRIGGER message_control_coverage AFTER UPDATE ON conversation_message_controls
 BEGIN
 UPDATE conversation_coverage SET version=version+1 WHERE singleton=1;
 INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version;
 END;
CREATE INDEX outbox_conversation_message ON outbox(json_extract(payload_json,'$.message_id'),seq) WHERE destination='main_thread';

-- Presentation order belongs to blocks; the audit sequence is unchanged.
CREATE TABLE conversation_blocks (
 position INTEGER PRIMARY KEY AUTOINCREMENT,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id) ON DELETE CASCADE,
 notice_only INTEGER NOT NULL DEFAULT 0,
 UNIQUE(message_id,notice_only)
) STRICT;
INSERT INTO conversation_blocks(message_id,notice_only)
 SELECT message_id,notice_only FROM (
  SELECT m.message_id,CASE WHEN m.author='system' THEN 1 ELSE 0 END AS notice_only,
   CASE WHEN m.author='system' THEN m.created_at ELSE COALESCE((SELECT MIN(t.created_at) FROM main_turns t WHERE t.message_id=m.message_id AND t.status<>'rejected'),m.created_at) END AS at,m.seq
  FROM conversation_messages m
  WHERE m.author='system' OR (m.author='main_agent' AND COALESCE(m.reply_to,'')='')
   OR (m.author='owner' AND (NOT EXISTS(SELECT 1 FROM outbox o WHERE o.destination='main_thread' AND json_extract(o.payload_json,'$.message_id')=m.message_id AND o.status IN ('pending','sending'))
    OR EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=m.message_id AND t.status IN ('started','completed','interrupted','failed','unknown'))))
  UNION ALL
  SELECT m.message_id,0,MIN(t.created_at),m.seq FROM conversation_messages m JOIN main_turns t ON t.message_id=m.message_id
  WHERE m.author='system' AND t.status IN ('started','completed','interrupted','failed','unknown') GROUP BY m.message_id
  UNION ALL
  SELECT m.message_id,0,MIN(r.created_at),m.seq FROM conversation_messages m JOIN conversation_messages r ON r.reply_to=m.message_id AND r.author='main_agent'
  WHERE m.author='system' AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=m.message_id AND t.status IN ('started','completed','interrupted','failed','unknown')) GROUP BY m.message_id
 ) ORDER BY at,seq,notice_only DESC;
CREATE TRIGGER conversation_block_notice AFTER INSERT ON conversation_messages
 WHEN NEW.author='system' OR (NEW.author='main_agent' AND COALESCE(NEW.reply_to,'')='')
 BEGIN INSERT OR IGNORE INTO conversation_blocks(message_id,notice_only) VALUES(NEW.message_id,CASE WHEN NEW.author='system' THEN 1 ELSE 0 END); END;
CREATE TRIGGER conversation_block_started AFTER INSERT ON events
 WHEN NEW.kind='conversation.message_state' AND json_extract(NEW.body_json,'$.state') IN ('processing','replied','interrupted','failed','unknown')
 AND EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=NEW.object_id)
 AND NOT EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=NEW.object_id AND withdrawn_at<>0)
 BEGIN INSERT OR IGNORE INTO conversation_blocks(message_id) VALUES(NEW.object_id); END;
CREATE INDEX conversation_replies ON conversation_messages(reply_to,seq);

-- Addon replies can have no hosted turn; keep their source and body reachable.
CREATE TRIGGER conversation_block_reply AFTER INSERT ON conversation_messages
 WHEN NEW.author='main_agent' AND EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=NEW.reply_to AND author='system')
 BEGIN INSERT OR IGNORE INTO conversation_blocks(message_id) VALUES(NEW.reply_to); END;
