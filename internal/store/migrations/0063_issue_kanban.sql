ALTER TABLE issue_controls ADD COLUMN work_status TEXT NOT NULL DEFAULT ''
 CHECK(work_status IN ('','backlog','todo','in_progress','in_review'));

CREATE TABLE issue_notification_settings (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 delay_minutes INTEGER NOT NULL CHECK(delay_minutes BETWEEN 0 AND 30),
 revision INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
INSERT INTO issue_notification_settings VALUES(1,3,0,0);

-- This is a debounce buffer, not a second delivery queue. Ready notices enter
-- the existing background/outbox path in the same transaction that drains it.
CREATE TABLE issue_status_notifications (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 baseline TEXT NOT NULL,
 latest TEXT NOT NULL,
 last_changed_at INTEGER NOT NULL,
 due_at INTEGER NOT NULL
) STRICT;
CREATE INDEX issue_status_notifications_due ON issue_status_notifications(due_at,issue_id);
