-- Processing receipts describe exact admitted versions, not semantic approval.
CREATE TABLE logical_turn_processing (
    logical_turn_id TEXT NOT NULL REFERENCES logical_turns(logical_turn_id),
    message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
    content_revision INTEGER NOT NULL,
    association_revision INTEGER NOT NULL,
    processed_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (logical_turn_id, message_id, content_revision, association_revision)
);

CREATE TRIGGER logical_turn_processing_admission
AFTER INSERT ON logical_turn_messages
BEGIN
    INSERT OR IGNORE INTO logical_turn_processing
      (logical_turn_id,message_id,content_revision,association_revision)
    VALUES (NEW.logical_turn_id,NEW.message_id,
      COALESCE((SELECT content_revision FROM message_dispositions WHERE message_id=NEW.message_id),1),
      COALESCE((SELECT revision FROM message_association_revisions WHERE message_id=NEW.message_id),0));
END;

-- Streaming revisions preserve their own delivery position without certifying
-- facts or reopening a completed model turn.
CREATE TRIGGER logical_turn_processing_content
AFTER UPDATE OF content_revision ON message_dispositions
WHEN NEW.content_revision<>OLD.content_revision
BEGIN
 INSERT OR IGNORE INTO logical_turn_processing(logical_turn_id,message_id,content_revision,association_revision)
 SELECT logical_turn_id,NEW.message_id,NEW.content_revision,
 COALESCE((SELECT revision FROM message_association_revisions WHERE message_id=NEW.message_id),0)
 FROM logical_turn_messages WHERE message_id=NEW.message_id;
END;
