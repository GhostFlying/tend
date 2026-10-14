-- Core ledger schema. Times are Unix milliseconds from the injected clock.
-- Enum values are validated in Go; JSON columns hold protocol-validated
-- documents. Never edit this file after it is merged; add a new migration.

CREATE TABLE request_records (
  domain            TEXT    NOT NULL,
  request_id        TEXT    NOT NULL,
  operation         TEXT    NOT NULL,
  payload_hash      TEXT    NOT NULL,
  principal_json    TEXT    NOT NULL,
  parent_request_id TEXT,
  trace_id          TEXT    NOT NULL,
  status            TEXT    NOT NULL,
  result_json       TEXT,
  error_code        TEXT,
  error_message     TEXT,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  PRIMARY KEY (domain, request_id)
) STRICT;

CREATE INDEX request_records_trace ON request_records (trace_id);

CREATE TABLE tasks (
  task_id          TEXT    NOT NULL PRIMARY KEY,
  title            TEXT    NOT NULL,
  lifecycle        TEXT    NOT NULL,
  control_mode     TEXT    NOT NULL,
  control_revision INTEGER NOT NULL,
  spec_version     INTEGER NOT NULL,
  priority         INTEGER NOT NULL,
  block_reason     TEXT    NOT NULL,
  -- Generation of the latest implement action for the current spec version.
  implement_generation INTEGER NOT NULL DEFAULT 0,
  -- Artifact under acceptance for the current spec version, if any.
  current_artifact_id  TEXT,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
) STRICT;

CREATE TABLE task_specs (
  task_id           TEXT    NOT NULL REFERENCES tasks (task_id),
  spec_version      INTEGER NOT NULL,
  body_json         TEXT    NOT NULL,
  source_message_id TEXT,
  created_at        INTEGER NOT NULL,
  PRIMARY KEY (task_id, spec_version)
) STRICT;

CREATE TABLE task_dependencies (
  task_id    TEXT NOT NULL REFERENCES tasks (task_id),
  depends_on TEXT NOT NULL REFERENCES tasks (task_id),
  PRIMARY KEY (task_id, depends_on),
  CHECK (task_id <> depends_on)
) STRICT;

-- One row per planning commit derived from a source message; a replay of
-- the same commit under a new request ID hits the primary key.
CREATE TABLE plan_commits (
  source_message_id TEXT    NOT NULL,
  commit_seq        INTEGER NOT NULL,
  request_id        TEXT    NOT NULL,
  created_at        INTEGER NOT NULL,
  PRIMARY KEY (source_message_id, commit_seq)
) STRICT;

CREATE TABLE plan_actions (
  action_id           TEXT    NOT NULL PRIMARY KEY,
  task_id             TEXT    NOT NULL REFERENCES tasks (task_id),
  kind                TEXT    NOT NULL,
  -- kind|task_id|spec_version|artifact_id|generation: one action per effect.
  effect_key          TEXT    NOT NULL UNIQUE,
  trigger_json        TEXT    NOT NULL,
  target_spec_version INTEGER NOT NULL,
  target_artifact_id  TEXT,
  authorization_json  TEXT    NOT NULL,
  generation          INTEGER NOT NULL,
  status              TEXT    NOT NULL,
  plan_commit_ref     TEXT,
  created_request_id  TEXT    NOT NULL,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
) STRICT;

CREATE INDEX plan_actions_task ON plan_actions (task_id, status);

CREATE TABLE runs (
  run_id            TEXT    NOT NULL PRIMARY KEY,
  task_id           TEXT    NOT NULL REFERENCES tasks (task_id),
  action_id         TEXT    NOT NULL REFERENCES plan_actions (action_id),
  generation        INTEGER NOT NULL,
  role              TEXT    NOT NULL,
  host_id           TEXT,
  native_json       TEXT    NOT NULL DEFAULT '{}',
  desired_state     TEXT    NOT NULL,
  observed_state    TEXT    NOT NULL,
  result_json       TEXT,
  last_confirmed_at INTEGER,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  UNIQUE (action_id, generation)
) STRICT;

CREATE TABLE artifacts (
  artifact_id TEXT    NOT NULL PRIMARY KEY,
  task_id     TEXT    NOT NULL REFERENCES tasks (task_id),
  run_id      TEXT    NOT NULL REFERENCES runs (run_id),
  kind        TEXT    NOT NULL,
  ref_json    TEXT    NOT NULL,
  digest      TEXT    NOT NULL,
  created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE acceptances (
  acceptance_id TEXT    NOT NULL PRIMARY KEY,
  task_id       TEXT    NOT NULL REFERENCES tasks (task_id),
  run_id        TEXT    NOT NULL REFERENCES runs (run_id),
  spec_version  INTEGER NOT NULL,
  artifact_id   TEXT    NOT NULL REFERENCES artifacts (artifact_id),
  verdict       TEXT    NOT NULL,
  evidence_json TEXT    NOT NULL,
  issues_json   TEXT    NOT NULL,
  created_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE decisions (
  decision_id         TEXT    NOT NULL PRIMARY KEY,
  task_id             TEXT    REFERENCES tasks (task_id),
  question            TEXT    NOT NULL,
  options_json        TEXT    NOT NULL,
  scope               TEXT    NOT NULL,
  version             INTEGER NOT NULL,
  status              TEXT    NOT NULL,
  asked_message_id    TEXT,
  answered_message_id TEXT,
  answer_json         TEXT,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
) STRICT;

CREATE TABLE approvals (
  approval_id      TEXT    NOT NULL PRIMARY KEY,
  action_id        TEXT    NOT NULL REFERENCES plan_actions (action_id),
  params_digest    TEXT    NOT NULL,
  spec_version     INTEGER NOT NULL,
  control_revision INTEGER NOT NULL,
  expires_at       INTEGER,
  status           TEXT    NOT NULL,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
) STRICT;

CREATE TABLE conversation_messages (
  seq         INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  message_id  TEXT    NOT NULL UNIQUE,
  author      TEXT    NOT NULL,
  client_id   TEXT,
  body        TEXT    NOT NULL,
  reply_to    TEXT,
  last_seen_seq INTEGER,
  request_id  TEXT,
  created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE conversation_bindings (
  message_id  TEXT NOT NULL REFERENCES conversation_messages (message_id),
  object_type TEXT NOT NULL,
  object_id   TEXT NOT NULL,
  kind        TEXT NOT NULL,
  PRIMARY KEY (message_id, object_type, object_id)
) STRICT;

-- event_seq is the cursor clients resume from.
CREATE TABLE events (
  event_seq      INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  event_id       TEXT    NOT NULL UNIQUE,
  source         TEXT    NOT NULL,
  object_type    TEXT    NOT NULL,
  object_id      TEXT    NOT NULL,
  object_version INTEGER,
  kind           TEXT    NOT NULL,
  body_json      TEXT    NOT NULL,
  request_id     TEXT,
  trace_id       TEXT,
  created_at     INTEGER NOT NULL
) STRICT;

CREATE INDEX events_object ON events (object_type, object_id, event_seq);

-- request_id is the child request ID allocated before the first send.
CREATE TABLE outbox (
  outbox_id       TEXT    NOT NULL PRIMARY KEY,
  request_id      TEXT    NOT NULL UNIQUE,
  parent_request_id TEXT,
  destination     TEXT    NOT NULL,
  operation       TEXT    NOT NULL,
  payload_json    TEXT    NOT NULL,
  status          TEXT    NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
) STRICT;

CREATE INDEX outbox_due ON outbox (status, next_attempt_at);

CREATE TABLE inbox (
  source          TEXT    NOT NULL,
  source_event_id TEXT    NOT NULL,
  received_at     INTEGER NOT NULL,
  PRIMARY KEY (source, source_event_id)
) STRICT;
