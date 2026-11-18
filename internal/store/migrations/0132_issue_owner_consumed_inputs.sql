-- An urgent receipt satisfies only the current version of the original input
-- in its own Issue conversation. Merely linked originals keep their own queue.
CREATE VIEW issue_owner_consumed_inputs AS
 SELECT s.source_message_id,s.message_id AS receipt_message_id,c.conversation_id
 FROM issue_owner_steers s
 JOIN conversation_messages m ON m.message_id=s.source_message_id
 JOIN agent_conversations c ON c.conversation_id=m.conversation_id AND c.work_item_id=s.issue_id
 LEFT JOIN message_dispositions d ON d.message_id=m.message_id
 WHERE s.status='consumed' AND s.native_item_id<>'' AND m.author='owner'
 AND s.source_revision=COALESCE(d.content_revision,1);

INSERT OR IGNORE INTO conversation_blocks(message_id)
 SELECT i.source_message_id FROM issue_owner_consumed_inputs i
 JOIN conversation_messages receipt ON receipt.message_id=i.receipt_message_id
 WHERE NOT EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=i.source_message_id AND c.withdrawn_at<>0)
 ORDER BY receipt.seq;
