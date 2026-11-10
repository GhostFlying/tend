-- D219: owner self-execution reuses its real Task and Run as artifact producer.
CREATE TABLE issue_owner_results (
 artifact_id TEXT PRIMARY KEY REFERENCES artifacts(artifact_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL CHECK(assignment_epoch>0),
 required_independent INTEGER NOT NULL CHECK(required_independent IN (0,1)),
 policy_json TEXT NOT NULL,
 acceptance_id TEXT REFERENCES acceptances(acceptance_id),
 reviewer_artifact_id TEXT REFERENCES artifacts(artifact_id),
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX issue_owner_results_issue ON issue_owner_results(issue_id,created_at,artifact_id);

-- The existing row is the active binding. Settled generations remain immutable
-- here when the same collaboration Task continues with a genuinely new Run.
CREATE TABLE issue_delivery_execution_history (
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 generation INTEGER NOT NULL,
 duty_revision INTEGER NOT NULL,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 source_task_id TEXT NOT NULL REFERENCES tasks(task_id),
 candidate_json TEXT NOT NULL,
 duty_json TEXT NOT NULL,
 artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
 upgrade_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(task_id,generation,duty_revision)
) STRICT;
CREATE INDEX issue_delivery_execution_history_run ON issue_delivery_execution_history(run_id,created_at);
