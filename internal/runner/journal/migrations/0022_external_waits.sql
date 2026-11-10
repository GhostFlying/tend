CREATE TABLE external_waits (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 identity_json TEXT NOT NULL
);
CREATE TABLE session_stop_reports (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 envelope_json TEXT NOT NULL
);
