-- D135: rejected delegation is durable responsibility, not task completion.
-- No historical failure or consumption evidence is manufactured.
CREATE TABLE coordination_delegation_attempts (
 seq INTEGER PRIMARY KEY,
 domain TEXT NOT NULL,
 request_id TEXT NOT NULL,
 execution_id TEXT NOT NULL,
 logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
 operation TEXT NOT NULL CHECK(operation IN ('issues.create','issues.request_manager','issues.request_owner')),
 source_message_id TEXT NOT NULL,
 target_id TEXT NOT NULL,
 intent_key TEXT NOT NULL,
 succeeded INTEGER NOT NULL CHECK(succeeded IN (0,1)),
 error_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(domain,request_id),
 FOREIGN KEY(domain,request_id) REFERENCES request_records(domain,request_id)
  DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE INDEX coordination_delegations_turn ON coordination_delegation_attempts(logical_turn_id,seq);
