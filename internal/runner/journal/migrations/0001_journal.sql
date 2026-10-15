-- The runner's local journal. The runner writes here before it acts, so a
-- restarted runner knows what it holds and resends requests under their
-- original IDs. Never edit after merge; add a new migration.

-- Runs the runner holds. phase is claimed, started, submitting, releasing
-- or done; fenced marks a run whose worker was stopped because its lease
-- could not be renewed.
CREATE TABLE runs (
  run_id          TEXT    NOT NULL PRIMARY KEY,
  lease_id        TEXT    NOT NULL,
  run_token       TEXT    NOT NULL,
  assignment_json TEXT    NOT NULL,
  phase           TEXT    NOT NULL,
  fenced          INTEGER NOT NULL DEFAULT 0,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_seq        INTEGER NOT NULL DEFAULT 1,
  updated_at      INTEGER NOT NULL
) STRICT;

-- Requests whose request ID must survive a restart: claims, and the final
-- report of a run. run_id is set for requests sent with the run's
-- credential.
CREATE TABLE requests (
  request_id   TEXT    NOT NULL PRIMARY KEY,
  operation    TEXT    NOT NULL,
  run_id       TEXT,
  target_json  TEXT    NOT NULL,
  payload_json TEXT    NOT NULL,
  created_at   INTEGER NOT NULL
) STRICT;

-- Run events not yet acknowledged by tend.
CREATE TABLE events (
  run_id    TEXT    NOT NULL,
  seq       INTEGER NOT NULL,
  native_id TEXT,
  kind      TEXT    NOT NULL,
  body_json TEXT,
  PRIMARY KEY (run_id, seq)
) STRICT;
