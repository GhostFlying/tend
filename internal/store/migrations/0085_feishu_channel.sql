-- D162: an explicit private owner/chat binding and durable provider event IDs.
INSERT INTO messaging_channels(channel_id,status) VALUES('feishu','disabled');
CREATE TABLE feishu_binding (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 owner TEXT NOT NULL DEFAULT '', app_id TEXT NOT NULL DEFAULT '',
 tenant_key TEXT NOT NULL DEFAULT '', sender_id TEXT NOT NULL DEFAULT '',
 chat_id TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 1,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1))
) STRICT;
INSERT INTO feishu_binding(singleton) VALUES(1);
CREATE TABLE feishu_inputs (
 event_id TEXT PRIMARY KEY, external_message_id TEXT NOT NULL UNIQUE,
 request_id TEXT NOT NULL UNIQUE, payload TEXT NOT NULL, owner TEXT NOT NULL,
 binding_revision INTEGER NOT NULL, message_id TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE TABLE feishu_deliveries (
 delivery_id TEXT PRIMARY KEY REFERENCES messaging_deliveries(delivery_id),
 external_message_id TEXT NOT NULL DEFAULT ''
) STRICT;
