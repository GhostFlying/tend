-- Refresh notices are current scoped watermarks, not immutable business events.
CREATE TABLE refresh_stream_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 cursor INTEGER NOT NULL CHECK(cursor>=0),
 expired_through INTEGER NOT NULL CHECK(expired_through>=0 AND expired_through<=cursor)
) STRICT;
INSERT INTO refresh_stream_state VALUES(1,
 (SELECT COALESCE(MAX(event_seq),0) FROM events),
 (SELECT COALESCE(MAX(event_seq),0) FROM events WHERE kind IN ('token_usage.changed','storage.originals_changed','host.runtime_changed')));

CREATE TABLE refresh_notifications (
 kind TEXT NOT NULL,
 object_type TEXT NOT NULL,
 object_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 event_seq INTEGER NOT NULL UNIQUE,
 pending INTEGER NOT NULL CHECK(pending IN (0,1)),
 changed_at INTEGER NOT NULL,
 PRIMARY KEY(kind,object_type,object_id)
) STRICT;
INSERT INTO refresh_notifications
 SELECT kind,object_type,object_id,COUNT(*),MAX(event_seq),1,MAX(created_at)
 FROM events WHERE kind IN ('token_usage.changed','storage.originals_changed','host.runtime_changed')
 GROUP BY kind,object_type,object_id;
DELETE FROM events WHERE kind IN ('token_usage.changed','storage.originals_changed','host.runtime_changed');

-- AUTOINCREMENT also stays beyond removed/coalesced notices for direct inserts.
INSERT INTO sqlite_sequence(name,seq) SELECT 'events',cursor FROM refresh_stream_state
 WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='events');
UPDATE sqlite_sequence SET seq=MAX(seq,(SELECT cursor FROM refresh_stream_state)) WHERE name='events';
CREATE TRIGGER event_stream_cursor AFTER INSERT ON events BEGIN
 UPDATE refresh_stream_state SET cursor=MAX(cursor,NEW.event_seq) WHERE singleton=1;
END;

-- A canonical control version owns its reason. Legacy unique reasons remain.
ALTER TABLE work_item_revisions ADD COLUMN reason_control_revision INTEGER;
UPDATE work_item_revisions SET reason_control_revision=revision,reason_json=''
 WHERE EXISTS(SELECT 1 FROM issue_control_revisions c WHERE c.issue_id=work_item_id
 AND c.revision=work_item_revisions.revision AND c.reason_json=work_item_revisions.reason_json);
CREATE TRIGGER canonical_revision_reason AFTER INSERT ON issue_control_revisions BEGIN
 UPDATE work_item_revisions SET reason_control_revision=NEW.revision,reason_json=''
 WHERE work_item_id=NEW.issue_id AND revision=NEW.revision AND reason_json=NEW.reason_json;
END;

-- The log's own (run_id,seq) key is the business fact and deduplication key.
DELETE FROM inbox WHERE source LIKE 'run:%' AND EXISTS(
 SELECT 1 FROM run_logs l WHERE inbox.source='run:'||l.run_id AND inbox.source_event_id=CAST(l.seq AS TEXT));

-- Root-turn attention identity belongs to its actual durable business event.
ALTER TABLE events ADD COLUMN run_attention_key TEXT;
UPDATE events SET run_attention_key=CASE
 WHEN json_extract(body_json,'$.managed_turn_at')>0 THEN 'managed:'||json_extract(body_json,'$.managed_turn_at')
 WHEN COALESCE(json_extract(body_json,'$.root_turn.sessionId'),'')<>'' AND COALESCE(json_extract(body_json,'$.root_turn.turnId'),'')<>''
 THEN json_extract(body_json,'$.root_turn.sessionId')||'/'||json_extract(body_json,'$.root_turn.turnId')
 WHEN json_extract(body_json,'$.turn_at')>0 THEN CAST(json_extract(body_json,'$.turn_at') AS TEXT) END
 WHERE kind='run.attention_required';
CREATE UNIQUE INDEX root_attention_identity ON events(object_id,run_attention_key)
 WHERE kind='run.attention_required' AND run_attention_key IS NOT NULL;
CREATE UNIQUE INDEX manager_lease_problem_identity ON conversation_bindings(object_id)
 WHERE object_type='lease' AND kind='manager_lease_expired';
DELETE FROM inbox WHERE EXISTS(SELECT 1 FROM events e WHERE e.kind='run.attention_required'
 AND inbox.source='root-attention:'||e.object_id AND inbox.source_event_id=e.run_attention_key);
DELETE FROM inbox WHERE EXISTS(SELECT 1 FROM conversation_bindings l JOIN conversation_bindings r USING(message_id)
 WHERE l.object_type='lease' AND l.kind='manager_lease_expired' AND r.object_type='run'
 AND r.kind='manager_lease_expired' AND inbox.source='manager-lease-problem:'||r.object_id AND inbox.source_event_id=l.object_id);

-- Stop mutating retired assessments. Unique older facts stay in their original
-- historical tables without creating a new upgrade or archival prerequisite.
DROP TRIGGER invalidate_context_assessments;

CREATE TABLE coordination_counters (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 withdrawal_conflicts INTEGER NOT NULL CHECK(withdrawal_conflicts>=0)
) STRICT;
INSERT INTO coordination_counters SELECT 1,COUNT(*) FROM request_records
 WHERE operation='conversation.withdraw_message' AND status='failed' AND error_code IN ('stale_revision','invalid_transition');
