-- A question handoff ends one execution, never its delivery agreement.
ALTER TABLE run_question_contracts ADD COLUMN handoff_allowed INTEGER NOT NULL DEFAULT 0 CHECK(handoff_allowed IN (0,1));
CREATE TABLE run_question_handoffs (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 decision_id TEXT NOT NULL UNIQUE REFERENCES decisions(decision_id),
 thread_id TEXT NOT NULL DEFAULT '',
 stopped INTEGER NOT NULL DEFAULT 0 CHECK(stopped IN (0,1)),
 continuation_action_id TEXT UNIQUE REFERENCES plan_actions(action_id),
 successor_run_id TEXT UNIQUE REFERENCES runs(run_id)
) STRICT;
