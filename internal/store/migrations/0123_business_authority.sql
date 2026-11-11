-- D225: retain actual business authorization, not transport receipt mappings.
DROP TRIGGER issue_control_revision_immutable_update;
ALTER TABLE issue_control_revisions ADD COLUMN authorizing_owner TEXT NOT NULL DEFAULT '';
UPDATE issue_control_revisions SET authorizing_owner=COALESCE(
 CASE WHEN json_extract(actor_json,'$.kind')='owner' THEN json_extract(actor_json,'$.subject') END,
 (SELECT e.owner_subject FROM main_owner_effects e WHERE e.request_id=issue_control_revisions.request_id),
 '');
CREATE TRIGGER issue_control_revision_immutable_update BEFORE UPDATE ON issue_control_revisions
BEGIN SELECT RAISE(ABORT, 'issue control revisions are immutable'); END;

ALTER TABLE main_owner_message_authorities RENAME TO legacy_main_owner_message_authorities;
ALTER TABLE main_owner_effects RENAME TO legacy_main_owner_effects;
CREATE TABLE main_owner_effects (
 execution_id TEXT NOT NULL,
 effect_seq INTEGER NOT NULL CHECK(effect_seq > 0),
 step INTEGER NOT NULL CHECK(step > 0),
 call_id TEXT NOT NULL,
 owner_subject TEXT NOT NULL,
 PRIMARY KEY(execution_id,effect_seq,step)
) STRICT;
INSERT INTO main_owner_effects(execution_id,effect_seq,step,call_id,owner_subject)
SELECT execution_id,effect_seq,step,call_id,owner_subject FROM legacy_main_owner_effects;
CREATE TABLE main_owner_message_authorities (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 execution_id TEXT NOT NULL,
 effect_seq INTEGER NOT NULL,
 step INTEGER NOT NULL,
 owner_subject TEXT NOT NULL,
 actor_json TEXT NOT NULL,
 FOREIGN KEY(execution_id,effect_seq,step) REFERENCES main_owner_effects(execution_id,effect_seq,step)
) STRICT;
INSERT INTO main_owner_message_authorities
SELECT m.message_id,e.execution_id,e.effect_seq,e.step,m.owner_subject,m.actor_json
FROM legacy_main_owner_message_authorities m JOIN legacy_main_owner_effects e USING(request_id);
DROP VIEW owner_authority_messages;
CREATE VIEW owner_authority_messages AS
SELECT m.message_id FROM conversation_messages m WHERE m.author='owner'
UNION ALL
SELECT m.message_id FROM conversation_messages m
JOIN main_owner_message_authorities a USING(message_id) WHERE m.author='agent';
DROP TABLE legacy_main_owner_message_authorities;
DROP TABLE legacy_main_owner_effects;
CREATE TRIGGER main_owner_input_context AFTER INSERT ON main_owner_message_authorities BEGIN
 UPDATE work_item_contexts SET revision=revision+1
 WHERE work_item_id IN (SELECT work_item_id FROM message_work_items
  WHERE message_id=NEW.message_id AND source='conversation' AND active=1);
 INSERT INTO message_context_obligations(message_id,work_item_id)
 SELECT message_id,work_item_id FROM message_work_items
 WHERE message_id=NEW.message_id AND active=1
 ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1;
END;


DROP VIEW hosted_coordination_effects;
CREATE VIEW hosted_coordination_effects AS
SELECT execution_id,effect_seq FROM local_hosted_coordination_effects
UNION ALL SELECT execution_id,effect_seq FROM runner_coordination_effects;
DROP INDEX local_hosted_coordination_effects_request;
DROP INDEX runner_coordination_effects_request;
ALTER TABLE local_hosted_coordination_effects DROP COLUMN request_id;
ALTER TABLE runner_coordination_effects DROP COLUMN request_id;

-- Keep query identity, bounded state and true late arrival, not response archives.
ALTER TABLE server_delivery_queries ADD COLUMN late_result_at INTEGER NOT NULL DEFAULT 0;
UPDATE server_delivery_queries SET late_result_at=MAX(started_at,finished_at,1) WHERE late_result_json<>'';
ALTER TABLE server_delivery_queries DROP COLUMN result_json;
ALTER TABLE server_delivery_queries DROP COLUMN late_result_json;
ALTER TABLE retired_runner_delivery_queries ADD COLUMN late_report_at INTEGER NOT NULL DEFAULT 0;
UPDATE retired_runner_delivery_queries SET late_report_at=MAX(retired_at,1) WHERE late_report_json<>'';
ALTER TABLE retired_runner_delivery_queries DROP COLUMN late_report_json;
