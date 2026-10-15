-- Run leases and run logs (plan B7, D29).

-- One lease per run in the MVP. The run token is derived from lease_id with
-- the server secret and is never stored.
CREATE TABLE leases (
  lease_id   TEXT    NOT NULL PRIMARY KEY,
  run_id     TEXT    NOT NULL UNIQUE REFERENCES runs (run_id),
  host_id    TEXT    NOT NULL REFERENCES hosts (host_id),
  expires_at INTEGER NOT NULL,
  -- active, lapsed (expired without renewal; the run may still be alive),
  -- released (the run finished)
  status     TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX leases_expiry ON leases (status, expires_at);

CREATE INDEX runs_host ON runs (host_id, observed_state);
CREATE INDEX runs_task ON runs (task_id, role, observed_state);
CREATE INDEX plan_actions_pending ON plan_actions (status, kind);

-- Run facts that do not change state: harness items, progress and output.
-- Kept out of events so they do not flood the event stream.
CREATE TABLE run_logs (
  run_id     TEXT    NOT NULL REFERENCES runs (run_id),
  seq        INTEGER NOT NULL,
  native_id  TEXT,
  kind       TEXT    NOT NULL,
  body_json  TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, seq)
) STRICT;
