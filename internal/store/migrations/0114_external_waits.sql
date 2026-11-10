-- Machine waits preserve their original action and require physical stop proof.
CREATE TABLE run_external_waits (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 wait_id TEXT NOT NULL UNIQUE,
 wait_key TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 status TEXT NOT NULL CHECK(status IN ('stopping','waiting','continued','cancelled')),
 body_json TEXT NOT NULL,
 next_check_at INTEGER NOT NULL,
 continuation_action_id TEXT UNIQUE REFERENCES plan_actions(action_id),
 successor_run_id TEXT UNIQUE REFERENCES runs(run_id)
) STRICT;
CREATE INDEX external_waits_due ON run_external_waits(status,next_check_at);
CREATE INDEX external_waits_task ON run_external_waits(task_id,run_id);
CREATE TABLE run_session_stops (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 thread_id TEXT NOT NULL,
 stopped_at INTEGER NOT NULL
) STRICT;
