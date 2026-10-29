-- Manual injection preserves originals and links corrections as new sources.
CREATE TABLE manual_message_sources(
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 metadata_json TEXT NOT NULL
) STRICT;

-- A consultation pins a registered private hosted definition; it owns no Issue.
CREATE TABLE consultation_conversations (
 conversation_id TEXT PRIMARY KEY REFERENCES agent_conversations(conversation_id),
 title TEXT NOT NULL,
 private_snapshot TEXT NOT NULL
) STRICT;
