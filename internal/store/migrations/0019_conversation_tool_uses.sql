-- The main agent's tool uses join the conversation (D69). Each takes a
-- place in the conversation's order as a row of kind 'tool_use', whose
-- details live in conversation_tool_uses and change as the call runs.
ALTER TABLE conversation_messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'message';

CREATE TABLE conversation_tool_uses (
  message_id TEXT    NOT NULL PRIMARY KEY REFERENCES conversation_messages (message_id),
  thread_id  TEXT    NOT NULL,
  turn_id    TEXT    NOT NULL,
  item_id    TEXT    NOT NULL,
  tool       TEXT    NOT NULL,
  input      TEXT    NOT NULL,
  output     TEXT    NOT NULL,
  status     TEXT    NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (turn_id, item_id)
) STRICT;
