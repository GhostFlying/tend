-- Durable owner questions belong to one existing run/action/spec, never to a retry.
CREATE TABLE worker_questions (
  decision_id TEXT PRIMARY KEY REFERENCES decisions(decision_id),
  run_id TEXT NOT NULL REFERENCES runs(run_id),
  action_id TEXT NOT NULL REFERENCES plan_actions(action_id),
  spec_version INTEGER NOT NULL,
  question_key TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  evidence TEXT NOT NULL,
  UNIQUE(run_id, question_key)
) STRICT;
CREATE INDEX worker_questions_run ON worker_questions(run_id);
