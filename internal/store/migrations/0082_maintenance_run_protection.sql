-- Recovery-stop authorization preserves physical facts separately from business
-- completion. Linkage never removes these original run/lease bindings.
CREATE INDEX server_committed_maintenances_unlinked ON server_committed_maintenances(linked,parent_id);
CREATE TABLE maintenance_protected_runs (
 maintenance_id TEXT NOT NULL REFERENCES server_committed_maintenances(maintenance_id),
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 task_id TEXT NOT NULL,
 action_id TEXT NOT NULL,
 host_id TEXT NOT NULL,
 lease_id TEXT NOT NULL,
 original_requests_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(maintenance_id,run_id)
) STRICT;
CREATE INDEX maintenance_protected_runs_run ON maintenance_protected_runs(run_id,lease_id);
CREATE TABLE maintenance_physical_stops (
 request_domain TEXT NOT NULL,
 request_id TEXT NOT NULL,
 maintenance_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 lease_id TEXT NOT NULL,
 operation TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 last_sequence INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(request_domain,request_id)
) STRICT;

CREATE INDEX maintenance_physical_stops_run ON maintenance_physical_stops(run_id,lease_id);
