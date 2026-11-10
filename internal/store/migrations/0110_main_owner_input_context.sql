-- Keep source authorship and request namespaces independent of owner authority.
ALTER TABLE issue_control_revisions ADD COLUMN request_domain TEXT NOT NULL DEFAULT '';

DROP TRIGGER context_association_insert;
CREATE TRIGGER context_association_insert AFTER INSERT ON message_work_items WHEN NEW.source<>'conversation' OR EXISTS(SELECT 1 FROM owner_authority_messages WHERE message_id=NEW.message_id) BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=NEW.work_item_id;
END;

DROP TRIGGER context_obligation_insert;
CREATE TRIGGER context_obligation_insert AFTER INSERT ON message_work_items WHEN EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=NEW.message_id AND author<>'system' AND (EXISTS(SELECT 1 FROM owner_authority_messages om WHERE om.message_id=conversation_messages.message_id) OR (conversation_id='main' AND NOT (author='main_agent' AND COALESCE((SELECT conversation_id FROM conversation_messages parent WHERE parent.message_id=conversation_messages.reply_to),'main')<>'main')))) BEGIN
 INSERT INTO message_context_obligations(message_id,work_item_id) VALUES(NEW.message_id,NEW.work_item_id)
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;

-- The conversation association can precede the server's delegation proof.
-- Publish its context effects when that proof becomes visible in the same write.
CREATE TRIGGER main_owner_input_context AFTER INSERT ON main_owner_message_authorities BEGIN
 UPDATE work_item_contexts SET revision=revision+1
 WHERE work_item_id IN (SELECT work_item_id FROM message_work_items
  WHERE message_id=NEW.message_id AND source='conversation' AND active=1);
 INSERT INTO message_context_obligations(message_id,work_item_id)
 SELECT message_id,work_item_id FROM message_work_items
 WHERE message_id=NEW.message_id AND active=1
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;
