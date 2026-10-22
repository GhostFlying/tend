-- The harness which originally received the assignment, independent of later
-- runner configuration. Empty legacy values require conservative inference.
ALTER TABLE runs ADD COLUMN worker_kind TEXT NOT NULL DEFAULT ''
  CHECK (worker_kind IN ('', 'appserver', 'orca'));
