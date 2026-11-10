-- Original bytes are published and verified before their reference is committed.
-- Historical JSON remains inline until explicit bounded migration verifies it.
CREATE TABLE retained_original_bodies (
 sha256 TEXT PRIMARY KEY,
 archive_id TEXT NOT NULL,
 byte_offset INTEGER NOT NULL CHECK(byte_offset>=0),
 byte_length INTEGER NOT NULL CHECK(byte_length>=0),
 characters INTEGER NOT NULL CHECK(characters>=0)
) STRICT;
CREATE INDEX retained_original_bodies_archive ON retained_original_bodies(archive_id,byte_offset);
CREATE TABLE retained_body_migration (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 revision INTEGER NOT NULL DEFAULT 1,
 migrated_rows INTEGER NOT NULL DEFAULT 0,
 migrated_bytes INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL DEFAULT 0
) STRICT;
INSERT INTO retained_body_migration(singleton) VALUES(1);
CREATE TABLE retained_body_migration_exceptions (
 table_name TEXT NOT NULL,
 column_name TEXT NOT NULL,
 original_rowid INTEGER NOT NULL,
 sha256 TEXT NOT NULL,
 reason TEXT NOT NULL,
 PRIMARY KEY(table_name,column_name,original_rowid,sha256)
) STRICT;
CREATE TABLE retained_rollout_archives (
 source_key TEXT NOT NULL,
 sha256 TEXT NOT NULL,
 file_id TEXT NOT NULL REFERENCES conversation_attachments(file_id),
 indexed_until INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(source_key,sha256)
) STRICT;
CREATE TABLE retained_body_migration_positions (
 table_name TEXT NOT NULL,
 column_name TEXT NOT NULL,
 last_rowid INTEGER NOT NULL,
 PRIMARY KEY(table_name,column_name)
) STRICT;
ALTER TABLE retained_original_bodies ADD COLUMN canonical_sha256 TEXT NOT NULL DEFAULT '';
CREATE INDEX retained_original_bodies_canonical ON retained_original_bodies(canonical_sha256) WHERE canonical_sha256<>'';
CREATE TABLE retained_native_sources (
 host_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 archive_id TEXT NOT NULL,
 next_offset INTEGER NOT NULL DEFAULT 0,
 indexed_offset INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(host_id,source_id)
) STRICT;
CREATE TABLE retained_native_chunks (
 host_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 byte_offset INTEGER NOT NULL,
 byte_length INTEGER NOT NULL,
 sha256 TEXT NOT NULL,
 indexed_offset INTEGER NOT NULL,
 PRIMARY KEY(host_id,source_id,byte_offset),
 FOREIGN KEY(host_id,source_id) REFERENCES retained_native_sources(host_id,source_id)
) STRICT;
CREATE TABLE retained_native_source_runs (
 host_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 PRIMARY KEY(host_id,source_id,run_id),
 FOREIGN KEY(host_id,source_id) REFERENCES retained_native_sources(host_id,source_id)
) STRICT;
CREATE TABLE retained_body_recipes (
 sha256 TEXT PRIMARY KEY,
 recipe_json TEXT NOT NULL
) STRICT;
