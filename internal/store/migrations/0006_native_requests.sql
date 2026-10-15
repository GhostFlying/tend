-- Request IDs for calls that arrive with a native identity of their own,
-- such as a harness tool call (thread, turn, call) (D28). The first sight
-- of a native identity allocates a request ID; every later sight of the
-- same identity gets the same one.
CREATE TABLE native_requests (
  source     TEXT    NOT NULL,
  native_key TEXT    NOT NULL,
  request_id TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (source, native_key)
) STRICT;
