-- Physical slot release for an unknown Run requires proof of its exact
-- predecessor's exit. The Run, lease and workspace remain available for reconciliation.
CREATE TABLE runner_window_released_leases (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 lease_id TEXT NOT NULL UNIQUE REFERENCES leases(lease_id),
 authorization_id TEXT NOT NULL REFERENCES runner_retirements(authorization_id)
) STRICT;
