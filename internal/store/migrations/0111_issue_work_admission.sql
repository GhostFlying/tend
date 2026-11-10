ALTER TABLE issue_controls ADD COLUMN owner_selection_json TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_controls ADD COLUMN todo_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE issue_controls ADD COLUMN todo_revision INTEGER NOT NULL DEFAULT 0;

CREATE TABLE issue_pause_operations (
 request_id TEXT NOT NULL,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 issue_revision INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(issue_id,issue_revision)
);
CREATE TABLE issue_pause_tasks (
 issue_id TEXT NOT NULL,
 issue_revision INTEGER NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 control_revision INTEGER NOT NULL,
 PRIMARY KEY(issue_id,issue_revision,task_id),
 FOREIGN KEY(issue_id,issue_revision) REFERENCES issue_pause_operations(issue_id,issue_revision)
);
CREATE TABLE run_pause_contracts (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 handoff_allowed INTEGER NOT NULL CHECK(handoff_allowed IN (0,1))
);
CREATE TABLE run_pause_handoffs (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 request_id TEXT NOT NULL,
 issue_id TEXT NOT NULL,
 issue_revision INTEGER NOT NULL,
 control_revision INTEGER NOT NULL,
 stopped INTEGER NOT NULL DEFAULT 0 CHECK(stopped IN (0,1)),
 thread_id TEXT NOT NULL DEFAULT '',
 continuation_action_id TEXT UNIQUE REFERENCES plan_actions(action_id),
 successor_run_id TEXT UNIQUE REFERENCES runs(run_id),
 FOREIGN KEY(issue_id,issue_revision) REFERENCES issue_pause_operations(issue_id,issue_revision)
);
CREATE INDEX run_pause_unstopped ON run_pause_handoffs(stopped,run_id);

ALTER TABLE task_owner_contracts ADD COLUMN review_admission_revision INTEGER NOT NULL DEFAULT 1;

CREATE TABLE issue_owner_migrations (
 request_id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL,
 backup_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(issue_id,assignment_epoch)
);
