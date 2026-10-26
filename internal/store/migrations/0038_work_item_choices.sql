CREATE TABLE work_item_choices (
 choice_id TEXT PRIMARY KEY,
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 source_key TEXT NOT NULL,
 version INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('open','answered','withdrawn','superseded')),
 body_json TEXT NOT NULL,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(work_item_id,source_key)
) STRICT;
CREATE INDEX work_item_choices_status ON work_item_choices(work_item_id,status,created_at);
