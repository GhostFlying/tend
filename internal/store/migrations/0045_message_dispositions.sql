-- D113: exact content versions and explicit classifications for new messages.
CREATE TABLE message_dispositions (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id) ON DELETE CASCADE,
 content_revision INTEGER NOT NULL DEFAULT 1,
 disposition TEXT NOT NULL CHECK(disposition IN ('pending','linked','unassigned')),
 classified_revision INTEGER NOT NULL DEFAULT 0,
 facts_revision INTEGER NOT NULL DEFAULT 0,
 owner_locked INTEGER NOT NULL DEFAULT 0,
 source TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL
);
CREATE TRIGGER message_disposition_new AFTER INSERT ON conversation_messages WHEN NEW.kind='message' BEGIN
 INSERT INTO message_dispositions(message_id,disposition,classified_revision,facts_revision,source,updated_at)
 VALUES(NEW.message_id,CASE WHEN NEW.author='system' THEN 'unassigned' ELSE 'pending' END,
 CASE WHEN NEW.author='system' THEN 1 ELSE 0 END,CASE WHEN NEW.author='system' THEN 1 ELSE 0 END,
 CASE WHEN NEW.author='system' THEN 'program' ELSE 'pending' END,NEW.created_at);
 INSERT OR IGNORE INTO message_work_items(message_id,work_item_id,source,updated_at)
 SELECT NEW.message_id,work_item_id,'conversation',NEW.created_at FROM agent_conversations
 WHERE conversation_id=CASE WHEN NEW.author='main_agent' AND NEW.reply_to IS NOT NULL THEN
 (SELECT conversation_id FROM conversation_messages WHERE message_id=NEW.reply_to) ELSE NEW.conversation_id END AND work_item_id IS NOT NULL;
 UPDATE message_dispositions SET disposition='linked',classified_revision=1,source=COALESCE((SELECT source FROM message_work_items WHERE message_id=NEW.message_id AND active=1 LIMIT 1),'program') WHERE message_id=NEW.message_id
 AND EXISTS(SELECT 1 FROM message_work_items WHERE message_id=NEW.message_id AND active=1);
 UPDATE message_dispositions SET facts_revision=content_revision WHERE message_id=NEW.message_id AND source='conversation' AND NEW.author='main_agent';
END;
CREATE TRIGGER message_disposition_content AFTER UPDATE OF body ON conversation_messages WHEN NEW.kind='message' AND OLD.body<>NEW.body BEGIN
 UPDATE message_dispositions SET content_revision=content_revision+1,
 disposition=CASE WHEN owner_locked=1 OR source IN ('program','conversation') THEN disposition ELSE 'pending' END,
 classified_revision=CASE WHEN owner_locked=1 OR source IN ('program','conversation') THEN content_revision+1 ELSE 0 END,
 facts_revision=CASE WHEN source='conversation' AND NEW.author='main_agent' THEN content_revision+1 ELSE 0 END WHERE message_id=NEW.message_id;
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id IN
 (SELECT work_item_id FROM message_work_items WHERE message_id=NEW.message_id AND active=1 AND (NEW.conversation_id='main' OR NEW.author='owner')
 UNION SELECT w.work_item_id FROM work_items w JOIN work_item_context_facts f ON
 (f.scope='global' OR f.work_item_id=w.work_item_id OR (f.scope='project' AND f.project=w.project))
 WHERE f.source_message_id=NEW.message_id AND f.superseded=0);
END;
CREATE TRIGGER message_disposition_link AFTER INSERT ON message_work_items WHEN NEW.active=1 BEGIN
 UPDATE message_dispositions SET disposition='linked',classified_revision=content_revision,source=NEW.source
 WHERE message_id=NEW.message_id AND owner_locked=0;
END;
CREATE TRIGGER message_disposition_link_update AFTER UPDATE OF active ON message_work_items BEGIN
 UPDATE message_dispositions SET disposition='pending',classified_revision=0,facts_revision=0
 WHERE message_id=NEW.message_id AND owner_locked=0;
END;
CREATE TABLE message_disposition_history (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 content_revision INTEGER NOT NULL,association_revision INTEGER NOT NULL,
 disposition TEXT NOT NULL,actor TEXT NOT NULL,created_at INTEGER NOT NULL
);

-- Retain the removed side of a correction until its facts have been reconciled.
CREATE TABLE message_context_obligations (
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id) ON DELETE CASCADE,
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 needs_review INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(message_id,work_item_id)
);
CREATE TRIGGER context_obligation_insert AFTER INSERT ON message_work_items WHEN EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=NEW.message_id AND author<>'system' AND (author='owner' OR (conversation_id='main' AND NOT (author='main_agent' AND COALESCE((SELECT conversation_id FROM conversation_messages parent WHERE parent.message_id=conversation_messages.reply_to),'main')<>'main')))) BEGIN
 INSERT INTO message_context_obligations(message_id,work_item_id) VALUES(NEW.message_id,NEW.work_item_id)
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;
CREATE TRIGGER context_obligation_update AFTER UPDATE ON message_work_items BEGIN
 INSERT INTO message_context_obligations(message_id,work_item_id) VALUES(NEW.message_id,NEW.work_item_id)
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;
