-- Person-facing obligations are independent of assignment and transport inbox.
CREATE TABLE attentions (
 attention_id TEXT PRIMARY KEY,
 issue_id TEXT REFERENCES issue_controls(issue_id),
 decision_id TEXT UNIQUE REFERENCES decisions(decision_id),
 choice_id TEXT UNIQUE REFERENCES work_item_choices(choice_id),
 kind TEXT NOT NULL CHECK(kind IN ('decision','action','information')),
 status TEXT NOT NULL CHECK(status IN ('open','resolved','withdrawn')),
 revision INTEGER NOT NULL CHECK(revision>0),
 body_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX attentions_issue_state ON attentions(issue_id,status,attention_id);
CREATE TABLE attention_revisions (
 attention_id TEXT NOT NULL REFERENCES attentions(attention_id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 request_id TEXT NOT NULL,
 PRIMARY KEY(attention_id,revision)
) STRICT;
CREATE TRIGGER attention_history_update BEFORE UPDATE ON attention_revisions BEGIN SELECT RAISE(ABORT,'attention history is immutable'); END;
CREATE TRIGGER attention_history_delete BEFORE DELETE ON attention_revisions BEGIN SELECT RAISE(ABORT,'attention history is immutable'); END;
CREATE TABLE issue_deadlines (
 deadline_id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 revision INTEGER NOT NULL CHECK(revision>0),
 status TEXT NOT NULL CHECK(status IN ('active','cancelled')),
 body_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX issue_deadlines_issue ON issue_deadlines(issue_id,status,deadline_id);
CREATE TABLE issue_deadline_revisions (
 deadline_id TEXT NOT NULL REFERENCES issue_deadlines(deadline_id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 request_id TEXT NOT NULL,
 PRIMARY KEY(deadline_id,revision)
) STRICT;
CREATE TRIGGER deadline_history_update BEFORE UPDATE ON issue_deadline_revisions BEGIN SELECT RAISE(ABORT,'deadline history is immutable'); END;
CREATE TRIGGER deadline_history_delete BEFORE DELETE ON issue_deadline_revisions BEGIN SELECT RAISE(ABORT,'deadline history is immutable'); END;
CREATE TABLE planner_entries (
 entry_id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL CHECK(revision>0),
 kind TEXT NOT NULL CHECK(kind IN ('user_action','issue_priority')),
 issue_id TEXT REFERENCES issue_controls(issue_id),
 attention_id TEXT REFERENCES attentions(attention_id),
 rank INTEGER NOT NULL,
 body_json TEXT NOT NULL
) STRICT;
CREATE INDEX planner_entries_rank ON planner_entries(kind,rank,entry_id);
