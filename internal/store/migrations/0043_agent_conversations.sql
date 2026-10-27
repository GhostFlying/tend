-- D111: logical conversations and sourced work-item context.
CREATE TABLE agent_conversations (
 conversation_id TEXT PRIMARY KEY,
 work_item_id TEXT UNIQUE REFERENCES work_items(work_item_id),
 revision INTEGER NOT NULL DEFAULT 0,
 model TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, last_message_seq INTEGER NOT NULL DEFAULT 0
);
INSERT INTO agent_conversations(conversation_id,created_at) VALUES('main',0);
ALTER TABLE conversation_messages ADD COLUMN conversation_id TEXT NOT NULL DEFAULT 'main';
CREATE INDEX conversation_session_seq ON conversation_messages(conversation_id,seq);
CREATE TRIGGER conversation_inherit AFTER INSERT ON conversation_messages WHEN NEW.author='main_agent' AND NEW.reply_to IS NOT NULL BEGIN
 UPDATE conversation_messages SET conversation_id=(SELECT conversation_id FROM conversation_messages WHERE message_id=NEW.reply_to) WHERE message_id=NEW.message_id;
END;
CREATE TRIGGER conversation_latest AFTER INSERT ON conversation_messages WHEN NEW.kind='message' BEGIN
 UPDATE agent_conversations SET last_message_seq=MAX(last_message_seq,NEW.seq) WHERE conversation_id=CASE WHEN NEW.author='main_agent' AND NEW.reply_to IS NOT NULL THEN (SELECT conversation_id FROM conversation_messages WHERE message_id=NEW.reply_to) ELSE NEW.conversation_id END;
END;
CREATE TRIGGER conversation_moved AFTER UPDATE OF conversation_id ON conversation_messages WHEN NEW.kind='message' BEGIN
 UPDATE agent_conversations SET last_message_seq=MAX(last_message_seq,NEW.seq) WHERE conversation_id=NEW.conversation_id;
END;
CREATE TABLE message_work_items (
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 active INTEGER NOT NULL DEFAULT 1, owner_locked INTEGER NOT NULL DEFAULT 0,
 source TEXT NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY(message_id,work_item_id)
);
CREATE TABLE message_association_revisions(message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),revision INTEGER NOT NULL DEFAULT 0);
CREATE TABLE message_association_history(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,message_id TEXT NOT NULL, revision INTEGER NOT NULL,
 actor TEXT NOT NULL, before_json TEXT NOT NULL, after_json TEXT NOT NULL,created_at INTEGER NOT NULL
);
CREATE TABLE work_item_contexts(
 work_item_id TEXT PRIMARY KEY REFERENCES work_items(work_item_id),revision INTEGER NOT NULL DEFAULT 0,
 baseline_revision INTEGER NOT NULL DEFAULT 0,coverage TEXT NOT NULL DEFAULT '',gaps TEXT NOT NULL DEFAULT '',prepared_by TEXT NOT NULL DEFAULT ''
);
INSERT INTO work_item_contexts(work_item_id) SELECT work_item_id FROM work_items;
CREATE TRIGGER context_new_item AFTER INSERT ON work_items BEGIN
 INSERT INTO work_item_contexts(work_item_id) VALUES(NEW.work_item_id);
 INSERT OR IGNORE INTO message_work_items(message_id,work_item_id,source,updated_at)
 SELECT message_id,NEW.work_item_id,'explicit',NEW.created_at FROM conversation_bindings WHERE object_type='work_item' AND object_id=NEW.work_item_id;
END;
CREATE TRIGGER context_item_update AFTER UPDATE OF revision ON work_items BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=NEW.work_item_id;
END;
CREATE TRIGGER context_binding AFTER INSERT ON conversation_bindings WHEN NEW.object_type='work_item' BEGIN
 INSERT OR IGNORE INTO message_work_items(message_id,work_item_id,source,updated_at)
 SELECT NEW.message_id,NEW.object_id,'explicit',0 WHERE EXISTS(SELECT 1 FROM work_items WHERE work_item_id=NEW.object_id);
END;
INSERT OR IGNORE INTO message_work_items(message_id,work_item_id,source,updated_at)
 SELECT b.message_id,b.object_id,'historical',0 FROM conversation_bindings b JOIN work_items w ON w.work_item_id=b.object_id WHERE b.object_type='work_item';
CREATE TRIGGER context_association_insert AFTER INSERT ON message_work_items WHEN NEW.source<>'conversation' OR EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=NEW.message_id AND author='owner') BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=NEW.work_item_id;
END;
CREATE TRIGGER context_association_update AFTER UPDATE ON message_work_items BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=NEW.work_item_id;
 UPDATE work_item_context_facts SET needs_review=1 WHERE source_message_id=NEW.message_id AND work_item_id=NEW.work_item_id AND NEW.active=0;
END;
CREATE TABLE work_item_context_facts(
 fact_id TEXT PRIMARY KEY, work_item_id TEXT REFERENCES work_items(work_item_id),project TEXT NOT NULL DEFAULT '',scope TEXT NOT NULL,
 kind TEXT NOT NULL,body TEXT NOT NULL,source_message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 attribution TEXT NOT NULL, supersedes TEXT REFERENCES work_item_context_facts(fact_id),superseded INTEGER NOT NULL DEFAULT 0,
 needs_review INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL
);
CREATE TABLE agent_thread_history(
 thread_id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES agent_conversations(conversation_id),created_at INTEGER NOT NULL
);
CREATE TABLE agent_context_usage(
 conversation_id TEXT PRIMARY KEY REFERENCES agent_conversations(conversation_id),thread_id TEXT NOT NULL,
 revision INTEGER NOT NULL,snapshot_json TEXT NOT NULL,updated_at INTEGER NOT NULL
);
CREATE TABLE agent_compactions(
 operation_id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES agent_conversations(conversation_id),thread_id TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL,source TEXT NOT NULL,item_id TEXT NOT NULL DEFAULT '',started_after_seq INTEGER NOT NULL DEFAULT 0,before_json TEXT NOT NULL DEFAULT '',after_json TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX one_active_compaction ON agent_compactions(conversation_id) WHERE status IN ('queued','starting','running','unknown');
CREATE TABLE background_assessments(
 assessment_id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL,delivery_id TEXT NOT NULL UNIQUE,context_revision INTEGER NOT NULL,
 disposition TEXT NOT NULL,note TEXT NOT NULL,evidence_json TEXT NOT NULL,stale INTEGER NOT NULL DEFAULT 0,
 return_message_id TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL
);
CREATE TABLE conversation_return_links(message_id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL,source_message_id TEXT NOT NULL);

UPDATE agent_conversations SET last_message_seq=COALESCE((SELECT MAX(seq) FROM conversation_messages WHERE kind='message'),0) WHERE conversation_id='main';
CREATE TRIGGER conversation_validate BEFORE INSERT ON conversation_messages WHEN NOT EXISTS(SELECT 1 FROM agent_conversations WHERE conversation_id=NEW.conversation_id) BEGIN
 SELECT RAISE(ABORT,'unknown conversation');
END;
CREATE TABLE agent_rollout_events(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,conversation_id TEXT NOT NULL REFERENCES agent_conversations(conversation_id),
 thread_id TEXT NOT NULL,turn_id TEXT NOT NULL DEFAULT '',item_id TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL,
 data_json TEXT NOT NULL,created_at INTEGER NOT NULL
);
CREATE INDEX agent_rollout_session_seq ON agent_rollout_events(conversation_id,seq);
CREATE TABLE agent_turn_contexts(request_id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL,context_revision INTEGER NOT NULL,settled INTEGER NOT NULL DEFAULT 0);
CREATE TRIGGER invalidate_context_assessments AFTER UPDATE OF revision ON work_item_contexts BEGIN
 UPDATE background_assessments SET stale=1 WHERE conversation_id IN (SELECT conversation_id FROM agent_conversations WHERE work_item_id=NEW.work_item_id) AND context_revision<>NEW.revision;
END;
CREATE TRIGGER invalidate_correction_baseline AFTER UPDATE OF active ON message_work_items WHEN OLD.active<>NEW.active BEGIN
 UPDATE work_item_contexts SET baseline_revision=0,gaps='Message associations changed; reconcile source coverage and derived facts.' WHERE work_item_id=NEW.work_item_id;
END;
CREATE TRIGGER context_task_change AFTER UPDATE ON tasks BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id IN (SELECT work_item_id FROM work_item_task_links WHERE task_id=NEW.task_id);
END;
CREATE TRIGGER agent_message_association AFTER UPDATE OF conversation_id ON conversation_messages WHEN NEW.conversation_id<>'main' BEGIN
 INSERT OR IGNORE INTO message_work_items(message_id,work_item_id,source,updated_at) SELECT NEW.message_id,work_item_id,'conversation',NEW.created_at FROM agent_conversations WHERE conversation_id=NEW.conversation_id AND work_item_id IS NOT NULL;
END;

CREATE TRIGGER association_revision_insert AFTER INSERT ON message_work_items BEGIN
 INSERT INTO message_association_revisions(message_id,revision) VALUES(NEW.message_id,1) ON CONFLICT(message_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER association_revision_update AFTER UPDATE ON message_work_items BEGIN
 INSERT INTO message_association_revisions(message_id,revision) VALUES(NEW.message_id,1) ON CONFLICT(message_id) DO UPDATE SET revision=revision+1;
END;

-- Association changes are conversation content for summary coverage, including
-- agent edits that do not enqueue an owner-correction notice.
CREATE TRIGGER coverage_association_insert AFTER INSERT ON message_work_items BEGIN
 UPDATE conversation_coverage SET version=version+1 WHERE singleton=1;
 INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version;
END;
CREATE TRIGGER coverage_association_update AFTER UPDATE ON message_work_items BEGIN
 UPDATE conversation_coverage SET version=version+1 WHERE singleton=1;
 INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version;
END;
