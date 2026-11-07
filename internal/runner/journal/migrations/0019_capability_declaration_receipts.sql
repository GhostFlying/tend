-- Keep original assignment bytes immutable while appending authenticated
-- declaration receipts for the same run and frozen execution binding.
CREATE TABLE run_capability_receipts (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 binding_json TEXT NOT NULL
) STRICT;
