-- Sessions of the agents the owner works in (D45, D47). Their baselines and
-- packets use thread_key '<agent>:<session_id>'.
CREATE TABLE agent_sessions (
  agent         TEXT    NOT NULL,
  session_id    TEXT    NOT NULL,
  client_id     TEXT    NOT NULL,
  first_seen_at INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,
  PRIMARY KEY (agent, session_id)
) STRICT;

-- The caller's note on why it made a request (D48).
ALTER TABLE request_records ADD COLUMN reason TEXT;
