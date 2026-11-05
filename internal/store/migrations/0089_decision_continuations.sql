-- Answer facts and question-handoff scheduling authority have separate lives.
CREATE TABLE decision_continuations (
 decision_id TEXT PRIMARY KEY REFERENCES decisions(decision_id),
 revision INTEGER NOT NULL CHECK(revision > 0),
 mode TEXT NOT NULL CHECK(mode IN ('continue_now','wait','dependency')),
 policy_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('waiting','ready','continued')),
 dependency_task_id TEXT REFERENCES tasks(task_id),
 dependency_spec_version INTEGER,
 answer_request_id TEXT NOT NULL,
 resume_request_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX decision_continuation_dependencies ON decision_continuations(dependency_task_id)
 WHERE mode='dependency' AND state='waiting';
