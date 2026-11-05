-- Independent permission to resume one original run. Old maintenance protection
-- and old physical/native request outcomes remain immutable facts.
CREATE TABLE retained_run_continuations (
 request_domain TEXT NOT NULL,
 request_id TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 lease_id TEXT NOT NULL,
 task_revision INTEGER NOT NULL,
 protection_sha256 TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 activated_at INTEGER,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(request_domain,request_id)
) STRICT;
CREATE INDEX retained_run_continuations_run ON retained_run_continuations(run_id,created_at);
