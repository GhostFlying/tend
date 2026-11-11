-- D225: typed immutable measurements and shared attribution projections.
-- Facts retain their sequence, counters, ownership and conflicting variants.
-- Defer references only while replacing the parent with the same identity.
PRAGMA defer_foreign_keys=ON;
CREATE TABLE token_usage_contexts (
 context_id INTEGER PRIMARY KEY,
 provider TEXT NOT NULL,
 native_version TEXT NOT NULL,
 session_id TEXT NOT NULL,
 model_source TEXT NOT NULL,
 time_source TEXT NOT NULL,
 counter_source TEXT NOT NULL,
 purpose TEXT NOT NULL,
 parent_session_id TEXT NOT NULL,
 parent_thread_id TEXT NOT NULL,
 profile TEXT NOT NULL,
 model TEXT NOT NULL,
 subject TEXT NOT NULL,
 role TEXT NOT NULL,
 UNIQUE(provider,native_version,session_id,model_source,time_source,counter_source,purpose,parent_session_id,parent_thread_id,profile,model,subject,role)
) STRICT;
INSERT INTO token_usage_contexts(provider,native_version,session_id,model_source,time_source,counter_source,purpose,parent_session_id,parent_thread_id,profile,model,subject,role) SELECT DISTINCT COALESCE(json_extract(payload_json,'$.provider'),''),COALESCE(json_extract(payload_json,'$.native_version'),''),COALESCE(json_extract(payload_json,'$.session_id'),''),COALESCE(json_extract(payload_json,'$.model_source'),''),COALESCE(json_extract(payload_json,'$.time_source'),''),COALESCE(json_extract(payload_json,'$.counter_source'),''),COALESCE(json_extract(payload_json,'$.purpose'),''),COALESCE(json_extract(payload_json,'$.parent_session_id'),''),COALESCE(json_extract(payload_json,'$.parent_thread_id'),''),COALESCE(json_extract(payload_json,'$.profile'),''),COALESCE(json_extract(payload_json,'$.model'),''),subject,role FROM token_usage_facts;
CREATE TABLE token_usage_facts_compact (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 source TEXT NOT NULL, request_id TEXT NOT NULL, revision INTEGER NOT NULL,
 context_id INTEGER NOT NULL REFERENCES token_usage_contexts(context_id),
 record_id TEXT NOT NULL, response_id TEXT NOT NULL, thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL, root_turn_id TEXT NOT NULL,
 native_content_hash TEXT NOT NULL, ordinal INTEGER,
 settlement TEXT NOT NULL, occurred_at INTEGER,
 input INTEGER, cache_read INTEGER, cache_write INTEGER, output INTEGER, total INTEGER,
 received_at INTEGER NOT NULL
) STRICT;
INSERT INTO token_usage_facts_compact(seq,source,request_id,revision,context_id,record_id,response_id,thread_id,turn_id,root_turn_id,native_content_hash,ordinal,settlement,occurred_at,input,cache_read,cache_write,output,total,received_at)
 SELECT f.seq,f.source,f.request_id,f.revision,m.context_id,COALESCE(json_extract(f.payload_json,'$.record_id'),''),COALESCE(json_extract(f.payload_json,'$.request_id'),''),COALESCE(json_extract(f.payload_json,'$.thread_id'),''),COALESCE(json_extract(f.payload_json,'$.turn_id'),''),COALESCE(json_extract(f.payload_json,'$.root_turn_id'),''),COALESCE(json_extract(f.payload_json,'$.native_content_hash'),''),json_extract(f.payload_json,'$.ordinal'),COALESCE(json_extract(f.payload_json,'$.settlement'),''),json_extract(f.payload_json,'$.occurred_at'),json_extract(f.payload_json,'$.input'),json_extract(f.payload_json,'$.cache_read'),json_extract(f.payload_json,'$.cache_write'),json_extract(f.payload_json,'$.output'),json_extract(f.payload_json,'$.total'),f.received_at FROM token_usage_facts f JOIN token_usage_contexts m ON m.provider=COALESCE(json_extract(f.payload_json,'$.provider'),'') AND m.native_version=COALESCE(json_extract(f.payload_json,'$.native_version'),'') AND m.session_id=COALESCE(json_extract(f.payload_json,'$.session_id'),'') AND m.model_source=COALESCE(json_extract(f.payload_json,'$.model_source'),'') AND m.time_source=COALESCE(json_extract(f.payload_json,'$.time_source'),'') AND m.counter_source=COALESCE(json_extract(f.payload_json,'$.counter_source'),'') AND m.purpose=COALESCE(json_extract(f.payload_json,'$.purpose'),'') AND m.parent_session_id=COALESCE(json_extract(f.payload_json,'$.parent_session_id'),'') AND m.parent_thread_id=COALESCE(json_extract(f.payload_json,'$.parent_thread_id'),'') AND m.profile=COALESCE(json_extract(f.payload_json,'$.profile'),'') AND m.model=COALESCE(json_extract(f.payload_json,'$.model'),'') AND m.subject=f.subject AND m.role=f.role;
DROP TABLE token_usage_facts;
ALTER TABLE token_usage_facts_compact RENAME TO token_usage_facts;
CREATE INDEX token_usage_fact_identity ON token_usage_facts(source,request_id,revision);
CREATE INDEX token_usage_native_identity ON token_usage_facts(thread_id,turn_id,response_id);
CREATE VIEW token_usage_fact_details AS SELECT f.*,m.provider,m.native_version,m.session_id,
 m.model_source,m.time_source,m.counter_source,m.purpose,m.parent_session_id,m.parent_thread_id,
 m.profile,m.model,m.subject,m.role FROM token_usage_facts f JOIN token_usage_contexts m USING(context_id);

-- This is a rebuildable projection, not another copy of TokenUsageRecord.
DROP TABLE token_usage_summaries;
DROP TABLE token_usage_contributions;
CREATE TABLE token_usage_dimensions (
 dimension_id INTEGER PRIMARY KEY,
 subject TEXT NOT NULL,role TEXT NOT NULL,
 binding_run TEXT NOT NULL,binding_conversation TEXT NOT NULL,
 session_id TEXT NOT NULL,parent_session_id TEXT NOT NULL,
 model TEXT NOT NULL,provider TEXT NOT NULL,native_version TEXT NOT NULL,native INTEGER NOT NULL,
 execution_state TEXT NOT NULL,issue_path TEXT NOT NULL,task_id TEXT NOT NULL,task_title TEXT NOT NULL,
 run_id TEXT NOT NULL,agent TEXT NOT NULL,agent_id TEXT NOT NULL,categories TEXT NOT NULL,
 UNIQUE(subject,role,binding_run,binding_conversation,session_id,parent_session_id,model,provider,
 native_version,native,execution_state,issue_path,task_id,task_title,run_id,agent,agent_id,categories)
) STRICT;
CREATE TABLE token_usage_partitions (
 dimension_key INTEGER PRIMARY KEY,
 dimension_id INTEGER NOT NULL REFERENCES token_usage_dimensions(dimension_id),
 partition INTEGER NOT NULL,
 UNIQUE(dimension_id,partition)
) STRICT;
CREATE TABLE token_usage_contributions (
 fact_seq INTEGER PRIMARY KEY REFERENCES token_usage_facts(seq),
 source TEXT NOT NULL,request_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,turn_id TEXT NOT NULL,root_turn_id TEXT NOT NULL,
 parent_thread_id TEXT NOT NULL,session_id TEXT NOT NULL,
 run_id TEXT NOT NULL,task_id TEXT NOT NULL,conversation_id TEXT NOT NULL,
 dimension_key INTEGER NOT NULL REFERENCES token_usage_partitions(dimension_key),
 bucket INTEGER NOT NULL,occurred_at INTEGER,
 visible INTEGER NOT NULL,conflict INTEGER NOT NULL,native INTEGER NOT NULL,
 estimated_usd TEXT,estimated_model TEXT,
 UNIQUE(source,request_id)
) STRICT;
CREATE INDEX token_usage_contribution_bucket ON token_usage_contributions(dimension_key,bucket,visible,conflict,occurred_at);
CREATE INDEX token_usage_contribution_turn ON token_usage_contributions(thread_id,turn_id);
CREATE INDEX token_usage_contribution_root_turn ON token_usage_contributions(thread_id,root_turn_id);
CREATE INDEX token_usage_contribution_run ON token_usage_contributions(run_id);
CREATE INDEX token_usage_contribution_task ON token_usage_contributions(task_id);
CREATE INDEX token_usage_actor_thread ON token_usage_contributions(conversation_id,thread_id);
CREATE INDEX token_usage_actor_parent ON token_usage_contributions(conversation_id,parent_thread_id);
CREATE INDEX token_usage_actor_session ON token_usage_contributions(conversation_id,session_id);
CREATE INDEX token_usage_session_conflicts ON token_usage_contributions(session_id,conflict);
CREATE TABLE token_usage_summaries (
 dimension_key INTEGER NOT NULL REFERENCES token_usage_partitions(dimension_key),bucket INTEGER NOT NULL,
 requests INTEGER NOT NULL,final INTEGER NOT NULL,
 input INTEGER NOT NULL,input_known INTEGER NOT NULL,
 cache_read INTEGER NOT NULL,cache_read_known INTEGER NOT NULL,
 cache_write INTEGER NOT NULL,cache_write_known INTEGER NOT NULL,
 output INTEGER NOT NULL,output_known INTEGER NOT NULL,total INTEGER NOT NULL,total_known INTEGER NOT NULL,
 estimated_usd TEXT NOT NULL,estimated_requests INTEGER NOT NULL,
 conflicts INTEGER NOT NULL,legacy_requests INTEGER NOT NULL,
 first_at INTEGER,last_at INTEGER,max_seq INTEGER NOT NULL,
 PRIMARY KEY(dimension_key,bucket)
) STRICT, WITHOUT ROWID;
CREATE INDEX token_usage_summary_bucket ON token_usage_summaries(bucket);

-- A single latest valid catalog, with no quote archive or historical valuation.
DROP TRIGGER usage_projection_prices;
CREATE TABLE token_usage_latest_prices (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 collected_at INTEGER NOT NULL,catalog_json TEXT NOT NULL
) STRICT;
INSERT INTO token_usage_latest_prices SELECT 1,collected_at,catalog_json
 FROM token_usage_price_catalogs ORDER BY collected_at DESC LIMIT 1;
DROP TABLE token_usage_price_catalogs;
CREATE TRIGGER usage_projection_prices_insert AFTER INSERT ON token_usage_latest_prices
BEGIN
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;
CREATE TRIGGER usage_projection_prices_update AFTER UPDATE OF catalog_json ON token_usage_latest_prices
WHEN OLD.catalog_json IS NOT NEW.catalog_json
BEGIN
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;
INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE true
ON CONFLICT(source,request_id) DO NOTHING;
-- Replacing a referenced parent temporarily increments SQLite's deferred
-- violation counter. Validate the resulting graph before clearing that stale
-- counter; an actual orphan aborts and rolls back the whole migration.
CREATE TEMP TABLE usage_compact_fk_check(violations INTEGER CHECK(violations=0));
INSERT INTO usage_compact_fk_check SELECT COUNT(*) FROM pragma_foreign_key_check;
DROP TABLE usage_compact_fk_check;
PRAGMA defer_foreign_keys=OFF;
