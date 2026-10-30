ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'execution'
 CHECK(kind IN ('execution','issue_manager'));

CREATE TABLE issue_manager_tasks (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 category_id TEXT NOT NULL,
 expected_epoch INTEGER NOT NULL,
 handoff_id TEXT NOT NULL DEFAULT '',
 issue_paused INTEGER NOT NULL DEFAULT 0,
 assignment_epoch INTEGER,
 conversation_id TEXT,
 host_id TEXT,
 pin_json TEXT,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','assigned','retiring','completed','cancelled')),
 source_message_id TEXT NOT NULL DEFAULT '',
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX issue_manager_tasks_current ON issue_manager_tasks(issue_id)
 WHERE state IN ('pending','assigned');

-- Preserve historical local snapshots and expose one read model for both executors.
ALTER TABLE hosted_executions RENAME TO local_hosted_executions;
CREATE TABLE runner_coordination_executions (
 execution_id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL,
 role TEXT NOT NULL CHECK(role='issue_manager'),
 configuration_revision INTEGER NOT NULL CHECK(configuration_revision=0),
 agent_ref TEXT NOT NULL,
 private_snapshot TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
CREATE VIEW hosted_executions AS
 SELECT * FROM local_hosted_executions UNION ALL SELECT * FROM runner_coordination_executions;
ALTER TABLE hosted_coordination_effects RENAME TO local_hosted_coordination_effects;
CREATE TABLE runner_coordination_effects (
 execution_id TEXT NOT NULL REFERENCES runner_coordination_executions(execution_id),
 effect_seq INTEGER NOT NULL CHECK(effect_seq>0),
 request_id TEXT NOT NULL,
 PRIMARY KEY(execution_id,effect_seq)
) STRICT;
CREATE VIEW hosted_coordination_effects AS
 SELECT * FROM local_hosted_coordination_effects UNION ALL SELECT * FROM runner_coordination_effects;

CREATE TABLE manager_native_requests (
 request_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 method TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 result_json TEXT,
 error_json TEXT,
 status TEXT NOT NULL CHECK(status IN ('pending','started','completed','unknown')),
 created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX manager_native_requests_run ON manager_native_requests(run_id,created_at,request_id);

CREATE TABLE manager_native_events (
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 seq INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 applied INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,seq)
) STRICT;

-- One persistent per-issue transition. No native history is rewritten.
CREATE TABLE manager_handoffs (
 handoff_id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 from_epoch INTEGER NOT NULL,
 to_epoch INTEGER NOT NULL,
 stage TEXT NOT NULL CHECK(stage IN ('fencing','ready','claimed','cancelled')),
 reason TEXT NOT NULL,
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE manager_migrations (
 request_id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 backup_json TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE manager_runner_cutover (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 finalized INTEGER NOT NULL DEFAULT 0,
 request_id TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL
) STRICT;
INSERT INTO manager_runner_cutover(singleton,updated_at) VALUES(1,0);
