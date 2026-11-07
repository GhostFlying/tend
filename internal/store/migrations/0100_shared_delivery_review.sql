-- Keep every historical approval/execution byte while adding policy authority.
CREATE TABLE issue_delivery_duties_next (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 acceptance_id TEXT REFERENCES acceptances(acceptance_id),
 artifact_id TEXT REFERENCES artifacts(artifact_id),
 revision INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','awaiting_owner','authorized','deferred','executing','complete','report','ended')),
 body_json TEXT NOT NULL DEFAULT '{}',
 CHECK((acceptance_id IS NOT NULL) <> (artifact_id IS NOT NULL)),
 PRIMARY KEY(issue_id,task_id)
) STRICT;
INSERT INTO issue_delivery_duties_next SELECT * FROM issue_delivery_duties;
CREATE TABLE issue_delivery_executions_next (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 issue_id TEXT NOT NULL,
 source_task_id TEXT NOT NULL,
 duty_revision INTEGER NOT NULL,
 candidate_json TEXT NOT NULL,
 upgrade_id TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(issue_id,source_task_id) REFERENCES issue_delivery_duties_next(issue_id,task_id)
) STRICT;
INSERT INTO issue_delivery_executions_next SELECT * FROM issue_delivery_executions;
DROP TABLE issue_delivery_executions;
DROP TABLE issue_delivery_duties;
ALTER TABLE issue_delivery_duties_next RENAME TO issue_delivery_duties;
ALTER TABLE issue_delivery_executions_next RENAME TO issue_delivery_executions;

-- A provider target owns one formal reviewer and a bounded, append-only history.
CREATE TABLE shared_delivery_reviews (
 review_id TEXT PRIMARY KEY,
 provider TEXT NOT NULL,
 repository TEXT NOT NULL,
 number INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL CHECK(json_valid(body_json)),
 UNIQUE(provider,repository,number)
) STRICT;
CREATE TABLE shared_delivery_review_links (
 review_id TEXT NOT NULL REFERENCES shared_delivery_reviews(review_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 delivery_id TEXT NOT NULL,
 task_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(review_id,issue_id,delivery_id,task_id)
) STRICT;
CREATE INDEX shared_delivery_review_issue ON shared_delivery_review_links(issue_id,review_id);
CREATE TABLE shared_delivery_review_history (
 review_id TEXT NOT NULL REFERENCES shared_delivery_reviews(review_id),
 revision INTEGER NOT NULL,
 request_id TEXT NOT NULL,
 body_json TEXT NOT NULL CHECK(json_valid(body_json)),
 PRIMARY KEY(review_id,revision)
) STRICT;
CREATE TRIGGER shared_delivery_review_history_immutable BEFORE UPDATE ON shared_delivery_review_history
 BEGIN SELECT RAISE(ABORT,'shared review history is immutable'); END;
CREATE TRIGGER shared_delivery_review_history_retained BEFORE DELETE ON shared_delivery_review_history
 BEGIN SELECT RAISE(ABORT,'shared review history is retained'); END;

-- One original strategy clarification serves the Issue, not every candidate.
CREATE TABLE issue_pr_policy_questions (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 attention_id TEXT NOT NULL REFERENCES attentions(attention_id)
) STRICT;
