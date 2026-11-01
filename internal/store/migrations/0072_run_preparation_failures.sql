CREATE TABLE run_preparation_failures (
    run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
    failure_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
