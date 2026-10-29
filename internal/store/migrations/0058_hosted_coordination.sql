-- Named hosted definitions are accepted from the one server-local source.
-- Private instruction snapshots never form an owner API projection.
CREATE TABLE hosted_configuration_versions (
 revision INTEGER PRIMARY KEY CHECK(revision>0),
 environment_id TEXT NOT NULL,
 private_hash TEXT NOT NULL,
 private_json TEXT NOT NULL,
 public_json TEXT NOT NULL,
 main_agent_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 accepted_at INTEGER NOT NULL
) STRICT;
CREATE TRIGGER hosted_configuration_immutable_update BEFORE UPDATE ON hosted_configuration_versions
BEGIN SELECT RAISE(ABORT,'hosted configuration snapshots are immutable'); END;
CREATE TRIGGER hosted_configuration_immutable_delete BEFORE DELETE ON hosted_configuration_versions
BEGIN SELECT RAISE(ABORT,'hosted configuration snapshots are immutable'); END;
CREATE TABLE hosted_executions (
 execution_id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL,
 issue_id TEXT REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER,
 role TEXT NOT NULL CHECK(role IN ('main','issue_manager','consultation')),
 configuration_revision INTEGER NOT NULL REFERENCES hosted_configuration_versions(revision),
 agent_ref TEXT NOT NULL,
 private_snapshot TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 CHECK((role='issue_manager' AND issue_id IS NOT NULL AND assignment_epoch>0)
 OR (role<>'issue_manager' AND issue_id IS NULL AND assignment_epoch IS NULL))
) STRICT;
CREATE INDEX hosted_executions_issue ON hosted_executions(issue_id,assignment_epoch);
CREATE TABLE hosted_coordination_effects (
 execution_id TEXT NOT NULL REFERENCES hosted_executions(execution_id),
 effect_seq INTEGER NOT NULL CHECK(effect_seq>0),
 request_id TEXT NOT NULL,
 PRIMARY KEY(execution_id,effect_seq)
) STRICT;

-- Ordinary hosted receipt reconciliation uses the existing pool and durable
-- read-only backoff even when an assignment fence disallows new turns.
ALTER TABLE native_turn_receipts_pending ADD COLUMN next_reconcile_at INTEGER NOT NULL DEFAULT 0;
