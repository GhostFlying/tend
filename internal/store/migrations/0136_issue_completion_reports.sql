-- One explicit completion report per original owner request. Routing links alone
-- never opt an Issue in. The receipt survives reopen, reassignment and retries.
CREATE TABLE issue_completion_reports (
 source_message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 content_revision INTEGER NOT NULL,
 issue_ids_json TEXT NOT NULL CHECK(json_type(issue_ids_json)='array'),
 published_message_id TEXT UNIQUE REFERENCES messaging_messages(message_id)
) STRICT;

-- Removing a member from the source or editing/withdrawing that source retires
-- the pending agreement. An unrelated later association cannot re-arm it.
CREATE TRIGGER completion_report_unlink AFTER UPDATE OF active ON message_work_items
WHEN OLD.active=1 AND NEW.active=0 BEGIN
 DELETE FROM issue_completion_reports WHERE source_message_id=NEW.message_id
 AND published_message_id IS NULL AND NEW.work_item_id IN (SELECT value FROM json_each(issue_ids_json));
END;
CREATE TRIGGER completion_report_edit AFTER UPDATE OF content_revision ON message_dispositions
WHEN NEW.content_revision<>OLD.content_revision BEGIN
 DELETE FROM issue_completion_reports WHERE source_message_id=NEW.message_id AND published_message_id IS NULL;
END;
CREATE TRIGGER completion_report_withdraw AFTER UPDATE OF withdrawn_at ON conversation_message_controls
WHEN NEW.withdrawn_at<>0 BEGIN
 DELETE FROM issue_completion_reports WHERE source_message_id=NEW.message_id AND published_message_id IS NULL;
END;
-- Transport edits can also arrive as a distinct correcting input.
CREATE TRIGGER completion_report_correction AFTER INSERT ON messaging_messages
WHEN NEW.correction_of<>'' BEGIN
 DELETE FROM issue_completion_reports WHERE source_message_id=NEW.correction_of AND published_message_id IS NULL;
END;
