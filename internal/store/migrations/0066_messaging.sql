-- D131: communication references the original ledger; native output stays diagnostic.
CREATE TABLE messaging_channels (
 channel_id TEXT PRIMARY KEY, owner TEXT NOT NULL DEFAULT '', user_id INTEGER NOT NULL DEFAULT 0,
 bot_id INTEGER NOT NULL DEFAULT 0, chat_id INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1, updated_at INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO messaging_channels(channel_id,status) VALUES('web','ready'),('telegram','disabled');
CREATE TABLE messaging_messages (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 direction TEXT NOT NULL CHECK(direction IN ('input','publication','notification')),
 channel_id TEXT NOT NULL REFERENCES messaging_channels(channel_id),
 bot_id INTEGER NOT NULL DEFAULT 0, chat_id INTEGER NOT NULL DEFAULT 0,
 external_id INTEGER NOT NULL DEFAULT 0, reply_to TEXT NOT NULL DEFAULT '',
 correction_of TEXT NOT NULL DEFAULT '', purpose TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '',
 execution_id TEXT NOT NULL DEFAULT '', source_revision INTEGER NOT NULL DEFAULT 1
) STRICT;
CREATE INDEX messaging_external ON messaging_messages(bot_id,chat_id,external_id);
CREATE TABLE messaging_deliveries (
 delivery_id TEXT PRIMARY KEY, message_id TEXT NOT NULL REFERENCES messaging_messages(message_id),
 channel_id TEXT NOT NULL, part INTEGER NOT NULL, kind TEXT NOT NULL,
 bot_id INTEGER NOT NULL DEFAULT 0, chat_id INTEGER NOT NULL DEFAULT 0,
 reply_external_id INTEGER NOT NULL DEFAULT 0, external_id INTEGER NOT NULL DEFAULT 0,
 body TEXT NOT NULL DEFAULT '', file_id TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(message_id,channel_id,kind,part)
) STRICT;
CREATE INDEX messaging_delivery_external ON messaging_deliveries(bot_id,chat_id,external_id);
CREATE TABLE messaging_reads (
 message_id TEXT NOT NULL REFERENCES messaging_messages(message_id), content_revision INTEGER NOT NULL,
 reader TEXT NOT NULL, endpoint TEXT NOT NULL, evidence TEXT NOT NULL,
 execution_id TEXT NOT NULL DEFAULT '', read_at INTEGER NOT NULL,
 PRIMARY KEY(message_id,content_revision,reader,endpoint)
) STRICT;
CREATE TABLE telegram_inbox (
 bot_id INTEGER NOT NULL, update_id INTEGER NOT NULL, payload TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending', reason TEXT NOT NULL DEFAULT '',
 request_id TEXT NOT NULL UNIQUE, message_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
 PRIMARY KEY(bot_id,update_id)
) STRICT;
CREATE TABLE telegram_offsets (bot_id INTEGER PRIMARY KEY, next_offset INTEGER NOT NULL) STRICT;
CREATE TABLE telegram_file_requests (
 bot_id INTEGER NOT NULL, update_id INTEGER NOT NULL, position INTEGER NOT NULL,
 request_id TEXT NOT NULL UNIQUE, PRIMARY KEY(bot_id,update_id,position),
 FOREIGN KEY(bot_id,update_id) REFERENCES telegram_inbox(bot_id,update_id)
) STRICT;
ALTER TABLE messaging_channels ADD COLUMN mark_read_mode TEXT NOT NULL DEFAULT '' CHECK(mark_read_mode IN ('','message','cursor'));
ALTER TABLE messaging_deliveries ADD COLUMN binding_revision INTEGER NOT NULL DEFAULT 0;
CREATE TABLE messaging_sources (
 message_id TEXT NOT NULL REFERENCES messaging_messages(message_id),
 source_message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 PRIMARY KEY(message_id,source_message_id)
) STRICT;

-- Historical owner submissions have a provable local origin. Only the explicit
-- formal reply operation qualifies old assistant output for communication;
-- native narration and historical read/delivery outcomes are not inferred.
INSERT INTO messaging_messages(message_id,direction,channel_id,reply_to,source_revision)
 SELECT c.message_id,'input','web',COALESCE(c.reply_to,''),d.content_revision
 FROM conversation_messages c JOIN message_dispositions d USING(message_id)
 WHERE c.kind='message' AND c.author IN ('owner','external');
INSERT INTO messaging_messages(message_id,direction,channel_id,reply_to,purpose)
 SELECT c.message_id,'publication','web',COALESCE(c.reply_to,''),''
 FROM conversation_messages c
 WHERE c.kind='message' AND c.author='main_agent'
 AND EXISTS(SELECT 1 FROM request_records r WHERE r.request_id=c.request_id
 AND r.operation='conversation.reply' AND r.status='succeeded');
INSERT INTO messaging_sources(message_id,source_message_id)
 SELECT m.message_id,c.reply_to FROM messaging_messages m JOIN conversation_messages c USING(message_id)
 WHERE m.direction='publication' AND COALESCE(c.reply_to,'')<>'';

CREATE TRIGGER messaging_origin_immutable BEFORE UPDATE ON messaging_messages
BEGIN SELECT RAISE(ABORT,'communication origin and publication are immutable'); END;
ALTER TABLE messaging_channels ADD COLUMN health TEXT NOT NULL DEFAULT 'unobserved';
ALTER TABLE messaging_channels ADD COLUMN last_observed_at INTEGER NOT NULL DEFAULT 0;
UPDATE messaging_channels SET health='ready' WHERE channel_id='web';
-- Exact native-input witnesses survive reconnect without duplicating prose.
CREATE TABLE messaging_native_inputs (
 request_id TEXT PRIMARY KEY REFERENCES main_turns(request_id),
 message_id TEXT NOT NULL REFERENCES messaging_messages(message_id),
 content_revision INTEGER NOT NULL, input_hash TEXT NOT NULL
) STRICT;
-- Resolve notification ownership through the existing business objects without
-- promoting system notices to new technical sources.
CREATE VIEW messaging_issue_links AS
 SELECT message_id,work_item_id FROM message_work_items WHERE active=1
 UNION SELECT b.message_id,a.issue_id FROM conversation_bindings b JOIN attentions a ON a.attention_id=b.object_id WHERE b.object_type='attention' AND a.issue_id IS NOT NULL
 UNION SELECT b.message_id,a.issue_id FROM conversation_bindings b JOIN attentions a ON a.decision_id=b.object_id WHERE b.object_type='decision' AND a.issue_id IS NOT NULL
 UNION SELECT b.message_id,d.issue_id FROM conversation_bindings b JOIN issue_deadlines d ON d.deadline_id=b.object_id WHERE b.object_type='deadline'
 UNION SELECT b.message_id,f.work_item_id FROM conversation_bindings b JOIN followup_rules f ON f.rule_id=b.object_id WHERE b.object_type='followup' AND f.work_item_id IS NOT NULL;
