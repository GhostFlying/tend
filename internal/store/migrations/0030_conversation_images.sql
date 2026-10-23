CREATE TABLE conversation_images (
 image_id TEXT PRIMARY KEY,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id) ON DELETE CASCADE,
 position INTEGER NOT NULL,
 name TEXT NOT NULL,
 media_type TEXT NOT NULL,
 data BLOB NOT NULL,
 UNIQUE(message_id,position)
) STRICT;
CREATE TRIGGER coverage_conversation_images_insert AFTER INSERT ON conversation_images
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_images_update AFTER UPDATE ON conversation_images
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(NEW.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
CREATE TRIGGER coverage_conversation_images_delete AFTER DELETE ON conversation_images
 BEGIN UPDATE conversation_coverage SET version=version+1 WHERE singleton=1; INSERT INTO conversation_changes(message_id,last_version) VALUES(OLD.message_id,(SELECT version FROM conversation_coverage WHERE singleton=1)) ON CONFLICT(message_id) DO UPDATE SET last_version=excluded.last_version; END;
