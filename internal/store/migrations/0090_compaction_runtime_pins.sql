-- Private native compact identity, frozen before any external send.
-- JSON null is an explicit legacy runtime, distinct from a missing old pin.
CREATE TABLE agent_compaction_runtimes (
 operation_id TEXT PRIMARY KEY REFERENCES agent_compactions(operation_id),
 thread_id TEXT NOT NULL CHECK(thread_id<>''),
 private_snapshot TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
CREATE TRIGGER compaction_runtime_immutable_update BEFORE UPDATE ON agent_compaction_runtimes
 BEGIN SELECT RAISE(ABORT,'compaction runtime snapshots are immutable'); END;
CREATE TRIGGER compaction_runtime_immutable_delete BEFORE DELETE ON agent_compaction_runtimes
 BEGIN SELECT RAISE(ABORT,'compaction runtime snapshots are immutable'); END;
