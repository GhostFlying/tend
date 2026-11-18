-- D246: bind assessments to the observed candidate without changing prior records.
ALTER TABLE issue_progress_revisions ADD COLUMN delivery_json TEXT NOT NULL DEFAULT 'null'
 CHECK (json_valid(delivery_json));
