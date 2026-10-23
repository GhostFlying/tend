-- System-message metadata is separate from the immutable conversation body.
CREATE TABLE message_notices (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 notice_type TEXT NOT NULL,
 upgrade_id TEXT NOT NULL DEFAULT '',
 host_id TEXT NOT NULL DEFAULT ''
) STRICT;
