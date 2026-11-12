-- D222: a superseded live binding is history, even before a result or Run.
-- Missing provenance remains absent; no synthetic artifact or execution exists.
CREATE TABLE issue_delivery_execution_history_v2 (
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 generation INTEGER NOT NULL,
 duty_revision INTEGER NOT NULL,
 run_id TEXT REFERENCES runs(run_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 source_task_id TEXT NOT NULL REFERENCES tasks(task_id),
 candidate_json TEXT NOT NULL,
 duty_json TEXT NOT NULL,
 artifact_id TEXT REFERENCES artifacts(artifact_id),
 upgrade_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(task_id,generation,duty_revision)
) STRICT;
INSERT INTO issue_delivery_execution_history_v2
 SELECT * FROM issue_delivery_execution_history;
DROP TABLE issue_delivery_execution_history;
ALTER TABLE issue_delivery_execution_history_v2 RENAME TO issue_delivery_execution_history;
CREATE INDEX issue_delivery_execution_history_run ON issue_delivery_execution_history(run_id,created_at);
