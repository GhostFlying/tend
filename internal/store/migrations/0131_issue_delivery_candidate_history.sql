-- A candidate can be replaced while a newer refresh fences member notifications.
-- Index the immutable replacement event under the Issues linked when it occurred.
CREATE TABLE issue_delivery_candidate_history (
 issue_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 event_seq INTEGER NOT NULL REFERENCES events(event_seq),
 delivery_id TEXT NOT NULL,
 PRIMARY KEY(issue_id,event_seq)
) STRICT;
