-- Native locator metadata outlives pending events and finished run cleanup.
-- Deliberately has no foreign key to the transient execution journal.
CREATE TABLE rollout_sources (
 ordinal INTEGER PRIMARY KEY,
 deployment_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 turn_id TEXT NOT NULL DEFAULT '',
 path TEXT NOT NULL DEFAULT '',
 UNIQUE(deployment_id,run_id,session_id,turn_id)
) STRICT;
CREATE INDEX rollout_source_owner ON rollout_sources(session_id,turn_id,deployment_id,run_id);
