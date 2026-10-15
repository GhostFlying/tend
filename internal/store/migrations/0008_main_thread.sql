-- The main agent's front thread and the turns that handle conversation
-- messages (plan B12a).

-- One front thread per harness instance.
CREATE TABLE main_threads (
  instance   TEXT    NOT NULL PRIMARY KEY,
  thread_id  TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

-- One row per delivery of a message to the front thread, keyed by the
-- delivery's child request ID. status: starting (written before
-- turn/start, so a crash leaves a trace), started, completed, interrupted,
-- failed (started, then failed), rejected (turn/start refused; retryable).
CREATE TABLE main_turns (
  request_id TEXT    NOT NULL PRIMARY KEY,
  message_id TEXT    NOT NULL REFERENCES conversation_messages (message_id),
  thread_id  TEXT    NOT NULL,
  turn_id    TEXT,
  status     TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX main_turns_turn ON main_turns (thread_id, turn_id);
