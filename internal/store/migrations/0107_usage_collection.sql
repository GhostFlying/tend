ALTER TABLE token_usage_local_sources ADD COLUMN collection_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE token_usage_local_sources ADD COLUMN collection_state TEXT NOT NULL DEFAULT 'settling';
UPDATE token_usage_local_sources SET collection_state=CASE WHEN writer_stopped=1 AND coverage='caught_up' THEN 'retired' ELSE 'settling' END;
UPDATE token_usage_local_sources SET collection_json=json_object('state',collection_state,'epoch',1,'revision',1,'policy_version',1,'execution_evidence','unknown','stop_proof',CASE WHEN writer_stopped=1 THEN 'retained-writer-stopped' ELSE '' END,'retirement_reason',CASE WHEN collection_state='retired' THEN 'complete' ELSE '' END);
CREATE INDEX token_usage_local_collection ON token_usage_local_sources(collection_state,source_key);
CREATE INDEX token_usage_local_identity ON token_usage_local_sources(run_id,conversation_id,json_extract(source_json,'$.provider'),json_extract(source_json,'$.thread_id'));

CREATE TABLE token_usage_collection_history (
  source_key TEXT NOT NULL,
  epoch INTEGER NOT NULL,
  revision INTEGER NOT NULL,
  collection_json TEXT NOT NULL,
  source_json TEXT NOT NULL,
  cursors_json TEXT NOT NULL,
  recorded_at INTEGER NOT NULL,
  PRIMARY KEY(source_key,epoch,revision)
);
CREATE TABLE token_usage_remote_collection (
  source_key TEXT PRIMARY KEY NOT NULL,
  host_id TEXT NOT NULL,
  local_source_id TEXT NOT NULL,
  observation_sequence INTEGER NOT NULL,
  collection_json TEXT NOT NULL,
  observation_json TEXT NOT NULL,
  control_revision INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL
);
CREATE TABLE token_usage_collection_commands (
  request_id TEXT PRIMARY KEY NOT NULL,
  source_key TEXT NOT NULL,
  host_id TEXT NOT NULL,
  command_json TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX token_usage_collection_host_commands ON token_usage_collection_commands(host_id,status,created_at);
