CREATE TABLE manager_native_operations (
 request_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 request_json TEXT NOT NULL,
 reply_json TEXT,
 status TEXT NOT NULL CHECK(status IN ('received','started','completed'))
) STRICT;
CREATE TABLE manager_event_sequences (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 next_seq INTEGER NOT NULL
) STRICT;
