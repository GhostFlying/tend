-- Native ledger identity is independent of synthetic provider response IDs.
-- Legacy facts remain immutable; links remove only duplicate contributions.
CREATE TABLE token_usage_legacy_links (
 legacy_seq INTEGER PRIMARY KEY REFERENCES token_usage_facts(seq),
 native_source TEXT NOT NULL,
 native_request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE token_usage_bindings (
 source TEXT NOT NULL,
 request_id TEXT NOT NULL,
 run_id TEXT NOT NULL DEFAULT '',
 conversation_id TEXT NOT NULL DEFAULT '',
 host_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(source,request_id)
) STRICT;

CREATE TABLE token_usage_sources (
 source_key TEXT PRIMARY KEY,
 host_id TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '',
 conversation_id TEXT NOT NULL DEFAULT '',
 thread_id TEXT NOT NULL,
 session_id TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL,
 coverage TEXT NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX token_usage_native_identity ON token_usage_facts(
 json_extract(payload_json,'$.thread_id'),
 json_extract(payload_json,'$.turn_id'),
 json_extract(payload_json,'$.request_id')
);

-- Hosted histories remain executor-local and are never returned by owner views.
CREATE TABLE token_usage_local_sources (
 source_key TEXT PRIMARY KEY,
 run_id TEXT NOT NULL DEFAULT '',
 conversation_id TEXT NOT NULL,
 source_json TEXT NOT NULL,
 cursors_json TEXT NOT NULL DEFAULT '{}',
 coverage TEXT NOT NULL DEFAULT 'pending',
 writer_stopped INTEGER NOT NULL DEFAULT 0 CHECK(writer_stopped IN (0,1)),
 updated_at INTEGER NOT NULL
) STRICT;

-- Quotes are immutable snapshots; refreshing does not rewrite usage facts.
CREATE TABLE token_usage_price_catalogs (
 collected_at INTEGER PRIMARY KEY,
 catalog_json TEXT NOT NULL
) STRICT;

-- An overlap lacking a value match keeps the old contribution until reviewed.
-- Candidates retain the evidence even when a later request resolves the group.
CREATE TABLE token_usage_legacy_candidates (
 legacy_seq INTEGER NOT NULL REFERENCES token_usage_facts(seq),
 native_source TEXT NOT NULL,
 native_request_id TEXT NOT NULL,
 matched INTEGER NOT NULL CHECK(matched IN (0,1)),
 PRIMARY KEY(legacy_seq,native_source,native_request_id)
) STRICT;

ALTER TABLE token_usage_bindings ADD COLUMN proof TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage_bindings ADD COLUMN attribution_conflict INTEGER NOT NULL DEFAULT 0 CHECK(attribution_conflict IN (0,1));
CREATE TABLE token_usage_binding_revisions (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 source TEXT NOT NULL,
 request_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 conversation_id TEXT NOT NULL,
 host_id TEXT NOT NULL,
 proof TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
ALTER TABLE token_usage_sources ADD COLUMN purpose TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage_sources ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT '';
