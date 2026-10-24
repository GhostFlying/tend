-- Preserve old BLOBs until the resumable Go exporter confirms durable files.
ALTER TABLE deliverable_files ADD COLUMN externalized INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_images ADD COLUMN size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_images ADD COLUMN sha256 TEXT NOT NULL DEFAULT '';
CREATE TABLE conversation_attachments (
 file_id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 media_type TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size >= 0),
 sha256 TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
CREATE TABLE conversation_message_attachments (
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id) ON DELETE CASCADE,
 file_id TEXT NOT NULL REFERENCES conversation_attachments(file_id),
 position INTEGER NOT NULL,
 PRIMARY KEY(message_id,position),
 UNIQUE(message_id,file_id)
) STRICT;
CREATE INDEX conversation_attachment_refs ON conversation_message_attachments(file_id);
CREATE TRIGGER coverage_conversation_message_attachments_insert AFTER INSERT ON conversation_message_attachments
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_message_attachments_delete AFTER DELETE ON conversation_message_attachments
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
