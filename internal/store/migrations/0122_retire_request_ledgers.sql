-- D225: transport requests are not a second business ledger. Preserve actual
-- revision provenance and unprocessed provider-input outcomes before retirement.
ALTER TABLE issue_control_revisions ADD COLUMN operation TEXT NOT NULL DEFAULT '';
DROP TRIGGER issue_control_revision_immutable_update;
UPDATE issue_control_revisions SET operation=COALESCE((SELECT operation FROM request_records r WHERE r.request_id=issue_control_revisions.request_id AND (r.domain=issue_control_revisions.request_domain OR issue_control_revisions.request_domain='') LIMIT 1),'');
CREATE TRIGGER issue_control_revision_immutable_update BEFORE UPDATE ON issue_control_revisions
BEGIN SELECT RAISE(ABORT,'issue control revisions are immutable'); END;

ALTER TABLE feishu_inputs ADD COLUMN state TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE feishu_inputs ADD COLUMN failure_code TEXT NOT NULL DEFAULT '';
UPDATE feishu_inputs SET state=CASE WHEN message_id<>'' THEN 'succeeded' ELSE COALESCE((SELECT status FROM request_records r WHERE r.domain='owner' AND r.request_id=feishu_inputs.request_id),'pending') END,
 failure_code=COALESCE((SELECT error_code FROM request_records r WHERE r.domain='owner' AND r.request_id=feishu_inputs.request_id),'');

-- Keep the actual no-effect delegation failure facts, without a foreign key
-- to the retired transport ledger. Its original identity remains provenance.
CREATE TABLE coordination_delegation_attempts_business (
 seq INTEGER PRIMARY KEY,domain TEXT NOT NULL,request_id TEXT NOT NULL,
 execution_id TEXT NOT NULL,logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
 operation TEXT NOT NULL CHECK(operation IN ('issues.create','issues.request_manager','issues.request_owner')),
 source_message_id TEXT NOT NULL,target_id TEXT NOT NULL,intent_key TEXT NOT NULL,
 succeeded INTEGER NOT NULL CHECK(succeeded IN (0,1)),error_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,intent_json TEXT NOT NULL DEFAULT '',corrects_request_id TEXT NOT NULL DEFAULT '',
 UNIQUE(domain,request_id)
) STRICT;
INSERT INTO coordination_delegation_attempts_business SELECT * FROM coordination_delegation_attempts;
DROP TABLE coordination_delegation_attempts;
ALTER TABLE coordination_delegation_attempts_business RENAME TO coordination_delegation_attempts;
CREATE INDEX coordination_delegations_turn ON coordination_delegation_attempts(logical_turn_id,seq);
CREATE INDEX delegation_correction ON coordination_delegation_attempts(domain,corrects_request_id) WHERE succeeded=1;

ALTER TABLE runs ADD COLUMN native_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN native_thread_json TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN native_turn_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN native_close_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN native_close_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN native_closed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN native_activity INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN native_close_activity INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_tool_uses ADD COLUMN native_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_tool_uses ADD COLUMN native_delivery_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_tool_uses ADD COLUMN native_delivery_status TEXT NOT NULL DEFAULT '';
-- Go migration resolves immutable originals before promoting real execution
-- facts, preserving unresolved commands, and dropping both generic ledgers.

ALTER TABLE conversation_tool_uses ADD COLUMN native_result_output TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_tool_uses ADD COLUMN native_result_success INTEGER NOT NULL DEFAULT 0;

ALTER TABLE conversation_tool_uses ADD COLUMN native_delivery_error TEXT NOT NULL DEFAULT '';

DROP TABLE IF EXISTS followup_registrations;

-- Two parties finish the outstanding native-command handoff independently.
ALTER TABLE manager_native_requests ADD COLUMN coordinator_consumed INTEGER NOT NULL DEFAULT 0 CHECK(coordinator_consumed IN (0,1));
ALTER TABLE manager_native_requests ADD COLUMN runner_acked INTEGER NOT NULL DEFAULT 0 CHECK(runner_acked IN (0,1));

ALTER TABLE manager_native_requests ADD COLUMN completion_error TEXT NOT NULL DEFAULT '';
ALTER TABLE manager_native_requests ADD COLUMN completion_rejected INTEGER NOT NULL DEFAULT 0 CHECK(completion_rejected IN (0,1));

DELETE FROM retained_body_migration_exceptions WHERE table_name IN ('request_records','native_requests');

DROP TABLE IF EXISTS telegram_file_requests;
