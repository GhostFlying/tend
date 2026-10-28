-- D118: additive worker issues and immutable coordinator explanations.
ALTER TABLE worker_questions ADD COLUMN issues_json TEXT NOT NULL DEFAULT '[]';
CREATE TABLE decision_briefings (
 decision_id TEXT NOT NULL REFERENCES decisions(decision_id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 PRIMARY KEY(decision_id, revision)
);
