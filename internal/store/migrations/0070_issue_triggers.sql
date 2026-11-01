CREATE TABLE observation_jobs (
 issue_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 delivery_id TEXT NOT NULL,
 status TEXT NOT NULL,
 event_id TEXT NOT NULL DEFAULT '',
 due_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(issue_id,delivery_id)
) STRICT;
CREATE INDEX observation_jobs_ready ON observation_jobs(status,due_at,issue_id,delivery_id);

CREATE TABLE webhook_subscriptions (
 id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 revision INTEGER NOT NULL,
 status TEXT NOT NULL,
 body_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX webhook_subscriptions_issue ON webhook_subscriptions(issue_id,id);

CREATE TABLE trigger_events (
 id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 schedule_id TEXT NOT NULL,
 title TEXT NOT NULL,
 source TEXT NOT NULL,
 status TEXT NOT NULL,
 reason TEXT NOT NULL,
 request_id TEXT NOT NULL,
 message_id TEXT NOT NULL DEFAULT '',
 planned_at INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX trigger_events_issue ON trigger_events(issue_id,id);
CREATE INDEX trigger_events_source ON trigger_events(source,id);
CREATE TABLE trigger_steps (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 event_id TEXT NOT NULL REFERENCES trigger_events(id),
 kind TEXT NOT NULL,
 at INTEGER NOT NULL,
 detail TEXT NOT NULL
) STRICT;
CREATE INDEX trigger_steps_event ON trigger_steps(event_id,seq);

CREATE TABLE webhook_receipts (
 subscription_id TEXT NOT NULL REFERENCES webhook_subscriptions(id),
 delivery_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 event_id TEXT NOT NULL REFERENCES trigger_events(id),
 status TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(subscription_id,delivery_id)
) STRICT;
CREATE INDEX webhook_receipts_pending ON webhook_receipts(status,created_at);
