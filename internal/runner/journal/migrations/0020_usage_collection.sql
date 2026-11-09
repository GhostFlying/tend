ALTER TABLE native_usage_sources ADD COLUMN registration_queued INTEGER NOT NULL DEFAULT 0;
UPDATE native_usage_sources SET registration_queued=COALESCE(json_extract(state_json,'$.registration_queued'),0);
CREATE INDEX native_usage_unregistered ON native_usage_sources(registration_queued,source_id);
ALTER TABLE native_usage_sources ADD COLUMN collection_state TEXT NOT NULL DEFAULT 'settling';
ALTER TABLE native_usage_sources ADD COLUMN collection_report_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE native_usage_sources ADD COLUMN collection_report_sequence INTEGER NOT NULL DEFAULT 0;
CREATE INDEX native_usage_reports ON native_usage_sources(collection_report_pending,source_id);
UPDATE native_usage_sources SET writer_stopped=1 WHERE EXISTS(SELECT 1 FROM runs WHERE runs.run_id=native_usage_sources.run_id AND runs.stopped_confirmed=1);
UPDATE native_usage_sources SET collection_state=CASE WHEN writer_stopped=1 AND json_extract(state_json,'$.coverage')='caught_up' THEN 'retired' ELSE 'settling' END;
UPDATE native_usage_sources SET state_json=json_set(state_json,'$.collection',json_object('state',collection_state,'epoch',1,'revision',1,'policy_version',1,'execution_evidence','unknown','stop_proof',CASE WHEN writer_stopped=1 THEN 'retained-writer-stopped' ELSE '' END,'retirement_reason',CASE WHEN collection_state='retired' THEN 'complete' ELSE '' END));
CREATE INDEX native_usage_collection ON native_usage_sources(collection_state,source_id);
CREATE INDEX native_usage_identity ON native_usage_sources(run_id,json_extract(state_json,'$.source.provider'),json_extract(state_json,'$.source.thread_id'));
CREATE TABLE native_usage_collection_history (
  source_id TEXT NOT NULL,
  epoch INTEGER NOT NULL,
  revision INTEGER NOT NULL,
  state_json TEXT NOT NULL,
  recorded_at INTEGER NOT NULL,
  PRIMARY KEY(source_id,epoch,revision)
);
CREATE TABLE native_usage_collection_controls (
  request_id TEXT PRIMARY KEY NOT NULL,
  command_json TEXT NOT NULL,
  source_id TEXT NOT NULL,
  result TEXT NOT NULL,
  queued INTEGER NOT NULL DEFAULT 0
);
