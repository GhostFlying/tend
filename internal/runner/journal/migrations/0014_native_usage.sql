CREATE TABLE native_usage_sources (
 source_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL,
 writer_stopped INTEGER NOT NULL DEFAULT 0 CHECK(writer_stopped IN (0,1)),
 state_json TEXT NOT NULL
) STRICT;
