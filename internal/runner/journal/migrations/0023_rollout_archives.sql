CREATE TABLE rollout_archive_cursors (
 deployment_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 cursor_json TEXT NOT NULL,
 pending_json TEXT,
 next_cursor_json TEXT,
 PRIMARY KEY(deployment_id, thread_id),
 CHECK((pending_json IS NULL) = (next_cursor_json IS NULL))
);
