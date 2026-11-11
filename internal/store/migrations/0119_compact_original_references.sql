-- Keep historical public identities readable without storing their expanded
-- text in every window. Existing file offsets never change.
ALTER TABLE retained_original_bodies RENAME TO retained_original_bodies_old;
CREATE TABLE retained_original_archives (
 archive_key INTEGER PRIMARY KEY,
 archive_id TEXT NOT NULL UNIQUE
) STRICT;
INSERT INTO retained_original_archives(archive_id) SELECT DISTINCT archive_id FROM retained_original_bodies_old;
CREATE TABLE retained_original_windows (
 window_id INTEGER PRIMARY KEY,
 sha256 BLOB NOT NULL UNIQUE CHECK(length(sha256)=32),
 archive_key INTEGER NOT NULL REFERENCES retained_original_archives(archive_key),
 byte_offset INTEGER NOT NULL CHECK(byte_offset>=0),
 byte_length INTEGER NOT NULL CHECK(byte_length>=0),
 characters INTEGER NOT NULL CHECK(characters>=0),
 canonical_sha256 BLOB NOT NULL DEFAULT X'' CHECK(length(canonical_sha256) IN (0,32))
) STRICT;
INSERT INTO retained_original_windows(sha256,archive_key,byte_offset,byte_length,characters,canonical_sha256)
 SELECT unhex(b.sha256),a.archive_key,b.byte_offset,b.byte_length,b.characters,unhex(b.canonical_sha256)
 FROM retained_original_bodies_old b JOIN retained_original_archives a USING(archive_id);
DROP TABLE retained_original_bodies_old;
CREATE INDEX retained_original_windows_archive ON retained_original_windows(archive_key,byte_offset);
CREATE INDEX retained_original_windows_canonical ON retained_original_windows(canonical_sha256) WHERE canonical_sha256<>X'';
CREATE VIEW retained_original_bodies AS
 SELECT lower(hex(w.sha256)) sha256,a.archive_id,w.byte_offset,w.byte_length,w.characters,lower(hex(w.canonical_sha256)) canonical_sha256
 FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key);
-- A value is either an existing window with a presentation, or one shared
-- reconstruction rule in the same durable original packs. Business rows carry
-- only body_id. Legacy embedded rules remain readable until bounded migration.
CREATE TABLE retained_original_values (
 body_id INTEGER PRIMARY KEY,
 sha256 BLOB NOT NULL UNIQUE CHECK(length(sha256)=32),
 window_id INTEGER REFERENCES retained_original_windows(window_id),
 encoding TEXT NOT NULL DEFAULT '',
 recipe_window_id INTEGER REFERENCES retained_original_windows(window_id),
 characters INTEGER NOT NULL CHECK(characters>=0),
 CHECK((window_id IS NULL)<>(recipe_window_id IS NULL))
) STRICT;
DELETE FROM retained_body_migration_positions;

-- Confirmed derived inputs are no longer recovery payloads. Metadata/watermarks
-- remain authoritative; unresolved inputs are migrated only after readback.
UPDATE context_packets SET body='' WHERE status='injected';
UPDATE main_thread_handoffs SET prompt='' WHERE phase='committed';

ALTER TABLE conversation_messages ADD COLUMN body_preview TEXT NOT NULL DEFAULT '';
UPDATE conversation_messages SET body_preview=substr(body,1,160);

ALTER TABLE retained_body_migration ADD COLUMN rewriting INTEGER NOT NULL DEFAULT 0 CHECK(rewriting IN (0,1));

DROP TRIGGER coverage_conversation_messages_update;
CREATE TRIGGER coverage_conversation_messages_update AFTER UPDATE ON conversation_messages WHEN (SELECT rewriting FROM retained_body_migration WHERE singleton=1)=0 AND (OLD.author IS NOT NEW.author OR OLD.client_id IS NOT NEW.client_id OR OLD.body IS NOT NEW.body OR OLD.reply_to IS NOT NEW.reply_to OR OLD.last_seen_seq IS NOT NEW.last_seen_seq OR OLD.request_id IS NOT NEW.request_id OR OLD.kind IS NOT NEW.kind OR OLD.decision_json IS NOT NEW.decision_json) AND COALESCE((SELECT conversation_id FROM conversation_messages WHERE message_id=NEW.reply_to AND NEW.author='main_agent'),NEW.conversation_id)='main'
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

DROP TRIGGER coverage_conversation_tool_uses_update;
CREATE TRIGGER coverage_conversation_tool_uses_update AFTER UPDATE ON conversation_tool_uses WHEN (SELECT rewriting FROM retained_body_migration WHERE singleton=1)=0 AND (OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.item_id IS NOT NEW.item_id OR OLD.tool IS NOT NEW.tool OR OLD.input IS NOT NEW.input OR OLD.output IS NOT NEW.output OR OLD.status IS NOT NEW.status) AND EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_messages parent ON parent.message_id=m.reply_to WHERE m.message_id=NEW.message_id AND CASE WHEN m.author='main_agent' AND parent.message_id IS NOT NULL THEN parent.conversation_id ELSE m.conversation_id END='main')
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;

-- Native source/run mappings keep their identities and accepted tail watermarks,
-- but share the archive registry and binary chunk digests as well.
ALTER TABLE retained_native_source_runs RENAME TO retained_native_source_runs_old;
ALTER TABLE retained_native_chunks RENAME TO retained_native_chunks_old;
ALTER TABLE retained_native_sources RENAME TO retained_native_sources_old;
INSERT OR IGNORE INTO retained_original_archives(archive_id) SELECT archive_id FROM retained_native_sources_old;
CREATE TABLE retained_native_source_index (
 host_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 archive_key INTEGER NOT NULL REFERENCES retained_original_archives(archive_key),
 next_offset INTEGER NOT NULL DEFAULT 0,
 indexed_offset INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(host_id,source_id)
) STRICT;
INSERT INTO retained_native_source_index SELECT s.host_id,s.source_id,s.thread_id,a.archive_key,s.next_offset,s.indexed_offset FROM retained_native_sources_old s JOIN retained_original_archives a USING(archive_id);
CREATE TABLE retained_native_chunk_index (
 host_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 byte_offset INTEGER NOT NULL,
 byte_length INTEGER NOT NULL,
 sha256 BLOB NOT NULL CHECK(length(sha256)=32),
 indexed_offset INTEGER NOT NULL,
 PRIMARY KEY(host_id,source_id,byte_offset),
 FOREIGN KEY(host_id,source_id) REFERENCES retained_native_source_index(host_id,source_id)
) STRICT;
INSERT INTO retained_native_chunk_index SELECT host_id,source_id,byte_offset,byte_length,unhex(sha256),indexed_offset FROM retained_native_chunks_old;
CREATE TABLE retained_native_source_runs (
 host_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 PRIMARY KEY(host_id,source_id,run_id),
 FOREIGN KEY(host_id,source_id) REFERENCES retained_native_source_index(host_id,source_id)
) STRICT;
INSERT INTO retained_native_source_runs SELECT * FROM retained_native_source_runs_old;
DROP TABLE retained_native_source_runs_old;
DROP TABLE retained_native_chunks_old;
DROP TABLE retained_native_sources_old;
CREATE VIEW retained_native_sources AS SELECT s.host_id,s.source_id,s.thread_id,a.archive_id,s.next_offset,s.indexed_offset FROM retained_native_source_index s JOIN retained_original_archives a USING(archive_key);
CREATE VIEW retained_native_chunks AS SELECT host_id,source_id,byte_offset,byte_length,lower(hex(sha256)) sha256,indexed_offset FROM retained_native_chunk_index;
