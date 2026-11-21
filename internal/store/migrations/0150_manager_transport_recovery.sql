-- One prospective recovery per confirmed transport stop. The action and wake
-- are admitted together and retain the original Run and stop identity.
CREATE TABLE manager_transport_recoveries (
 original_run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 stop_request_id TEXT NOT NULL,
 assignment_epoch INTEGER NOT NULL,
 issue_revision INTEGER NOT NULL,
 task_revision INTEGER NOT NULL,
 successor_generation INTEGER,
 wake_request_id TEXT UNIQUE,
 original_execution_id TEXT REFERENCES main_turns(request_id),
 CHECK((successor_generation IS NULL AND wake_request_id IS NULL AND original_execution_id IS NULL) OR
       (successor_generation IS NOT NULL AND wake_request_id IS NOT NULL AND original_execution_id IS NOT NULL))
) STRICT;
CREATE UNIQUE INDEX manager_transport_recovery_successor ON manager_transport_recoveries(task_id, successor_generation);
