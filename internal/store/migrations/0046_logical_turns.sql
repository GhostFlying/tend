-- D113: native executions may finish before the logical processing responsibility.
CREATE TABLE logical_turns (
 logical_turn_id TEXT PRIMARY KEY,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 status TEXT NOT NULL DEFAULT 'processing',
 repair_attempts INTEGER NOT NULL DEFAULT 0,
 reason TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL
);
CREATE TABLE logical_turn_executions (
 request_id TEXT PRIMARY KEY,
 logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
 settled INTEGER NOT NULL DEFAULT 0,
 repair INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE logical_turn_messages (
 logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 PRIMARY KEY(logical_turn_id,message_id)
);
CREATE TRIGGER logical_turn_reply AFTER INSERT ON conversation_agent_messages BEGIN
 INSERT INTO logical_turn_messages(logical_turn_id,message_id)
 SELECT e.logical_turn_id,NEW.message_id FROM logical_turn_executions e JOIN main_turns t ON t.request_id=e.request_id
 WHERE t.thread_id=NEW.thread_id AND t.turn_id=NEW.turn_id AND e.repair=0 ON CONFLICT(logical_turn_id,message_id) DO NOTHING;
 UPDATE logical_turns SET status='pending_repair',reason='A late reply requires review.' WHERE status='completed' AND logical_turn_id IN
 (SELECT logical_turn_id FROM logical_turn_messages WHERE message_id=NEW.message_id);
END;
CREATE TRIGGER logical_turn_late_binding AFTER UPDATE OF turn_id ON main_turns BEGIN
 INSERT INTO logical_turn_messages(logical_turn_id,message_id)
 SELECT e.logical_turn_id,m.message_id FROM logical_turn_executions e JOIN conversation_agent_messages m
 ON m.thread_id=NEW.thread_id AND m.turn_id=NEW.turn_id WHERE e.request_id=NEW.request_id AND e.repair=0 ON CONFLICT(logical_turn_id,message_id) DO NOTHING;
END;
CREATE TRIGGER logical_turn_content_changed AFTER UPDATE OF content_revision ON message_dispositions BEGIN
 UPDATE logical_turns SET status='pending_repair',reason='Message content changed after review.' WHERE logical_turn_id IN
 (SELECT logical_turn_id FROM logical_turn_messages WHERE message_id=NEW.message_id) AND status='completed';
END;
CREATE TABLE logical_turn_context_reviews (
 logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 revision INTEGER NOT NULL,
 PRIMARY KEY(logical_turn_id,work_item_id)
);
ALTER TABLE logical_turns ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER logical_turn_revision AFTER UPDATE OF status,repair_attempts ON logical_turns BEGIN
 UPDATE logical_turns SET revision=revision+1 WHERE logical_turn_id=NEW.logical_turn_id;
END;
CREATE INDEX associations_by_work_item ON message_work_items(work_item_id,active,message_id);

-- The set survives review so a removed association cannot escape final fencing.
CREATE TABLE logical_turn_contexts (
 logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 PRIMARY KEY(logical_turn_id,work_item_id)
);
CREATE TRIGGER logical_context_message AFTER INSERT ON logical_turn_messages BEGIN
 INSERT INTO logical_turn_contexts(logical_turn_id,work_item_id)
 SELECT NEW.logical_turn_id,work_item_id FROM (
 SELECT work_item_id FROM message_work_items WHERE message_id=NEW.message_id AND active=1
 UNION SELECT work_item_id FROM message_context_obligations WHERE message_id=NEW.message_id AND needs_review=1)
 WHERE 1 ON CONFLICT(logical_turn_id,work_item_id) DO NOTHING;
END;
CREATE TRIGGER logical_context_obligation AFTER INSERT ON message_context_obligations BEGIN
 INSERT INTO logical_turn_contexts(logical_turn_id,work_item_id)
 SELECT logical_turn_id,NEW.work_item_id FROM logical_turn_messages WHERE message_id=NEW.message_id
 ON CONFLICT(logical_turn_id,work_item_id) DO NOTHING;
END;
CREATE TRIGGER logical_context_obligation_updated AFTER UPDATE ON message_context_obligations WHEN NEW.needs_review=1 BEGIN
 INSERT INTO logical_turn_contexts(logical_turn_id,work_item_id)
 SELECT logical_turn_id,NEW.work_item_id FROM logical_turn_messages WHERE message_id=NEW.message_id
 ON CONFLICT(logical_turn_id,work_item_id) DO NOTHING;
END;
CREATE TRIGGER context_fact_obligation AFTER INSERT ON work_item_context_facts BEGIN
 INSERT INTO message_context_obligations(message_id,work_item_id)
 SELECT NEW.source_message_id,w.work_item_id FROM work_items w WHERE NEW.scope='global' OR w.work_item_id=NEW.work_item_id OR (NEW.scope='project' AND w.project=NEW.project)
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;
CREATE TRIGGER context_content_obligation AFTER UPDATE OF body ON conversation_messages WHEN NEW.body<>OLD.body BEGIN
 UPDATE work_item_context_facts SET needs_review=1 WHERE source_message_id=NEW.message_id AND superseded=0;
 INSERT INTO message_context_obligations(message_id,work_item_id)
 SELECT NEW.message_id,work_item_id FROM (
 SELECT work_item_id FROM message_work_items WHERE message_id=NEW.message_id AND active=1 AND (NEW.conversation_id='main' OR NEW.author='owner')
 UNION SELECT w.work_item_id FROM work_items w JOIN work_item_context_facts f ON
 (f.scope='global' OR f.work_item_id=w.work_item_id OR (f.scope='project' AND f.project=w.project))
 WHERE f.source_message_id=NEW.message_id AND f.superseded=0) WHERE 1
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;
ALTER TABLE logical_turns ADD COLUMN resumed_by TEXT REFERENCES logical_turns(logical_turn_id);

-- A disconnected process may have produced items not yet persisted by Tend.
CREATE TABLE native_turn_receipts_pending (
 request_id TEXT PRIMARY KEY REFERENCES main_turns(request_id)
);

-- Clarification adoption transfers a live responsibility, not a one-time copy.
CREATE TRIGGER logical_adopted_message AFTER INSERT ON logical_turn_messages BEGIN
 INSERT INTO logical_turn_messages(logical_turn_id,message_id)
 SELECT id,NEW.message_id FROM (
 WITH RECURSIVE successors(id) AS (
 SELECT resumed_by FROM logical_turns WHERE logical_turn_id=NEW.logical_turn_id AND resumed_by IS NOT NULL
 UNION SELECT l.resumed_by FROM logical_turns l JOIN successors s ON l.logical_turn_id=s.id WHERE l.resumed_by IS NOT NULL)
 SELECT id FROM successors) WHERE 1
 ON CONFLICT(logical_turn_id,message_id) DO NOTHING;
 UPDATE logical_turns SET status='pending_repair',reason='An adopted responsibility received a late message.'
 WHERE status='completed' AND logical_turn_id IN (
 WITH RECURSIVE successors(id) AS (
 SELECT resumed_by FROM logical_turns WHERE logical_turn_id=NEW.logical_turn_id AND resumed_by IS NOT NULL
 UNION SELECT l.resumed_by FROM logical_turns l JOIN successors s ON l.logical_turn_id=s.id WHERE l.resumed_by IS NOT NULL)
 SELECT id FROM successors);
END;
CREATE TRIGGER logical_adopted_context AFTER INSERT ON logical_turn_contexts BEGIN
 INSERT INTO logical_turn_contexts(logical_turn_id,work_item_id)
 SELECT id,NEW.work_item_id FROM (
 WITH RECURSIVE successors(id) AS (
 SELECT resumed_by FROM logical_turns WHERE logical_turn_id=NEW.logical_turn_id AND resumed_by IS NOT NULL
 UNION SELECT l.resumed_by FROM logical_turns l JOIN successors s ON l.logical_turn_id=s.id WHERE l.resumed_by IS NOT NULL)
 SELECT id FROM successors) WHERE 1
 ON CONFLICT(logical_turn_id,work_item_id) DO NOTHING;
END;
