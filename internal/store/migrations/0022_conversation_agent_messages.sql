-- Durable cumulative assistant text with stable conversation position (D74).
CREATE TABLE conversation_agent_messages (
  message_id TEXT NOT NULL PRIMARY KEY REFERENCES conversation_messages(message_id),
  thread_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('streaming', 'completed', 'interrupted', 'failed', 'unknown')),
  revision INTEGER NOT NULL CHECK (revision > 0),
  UNIQUE (thread_id, turn_id, item_id)
) STRICT;
