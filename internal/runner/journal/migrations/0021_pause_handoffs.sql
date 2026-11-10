CREATE TABLE pause_handoffs (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 identity_json TEXT NOT NULL
);
