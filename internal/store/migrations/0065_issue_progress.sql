-- D129: assessments follow aggregate revisions without replacing task facts.
CREATE TABLE issue_progress_revisions (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 revision INTEGER NOT NULL,
 summary TEXT NOT NULL,
 next_step TEXT NOT NULL,
 updated_at INTEGER NOT NULL,
 updated_by_json TEXT NOT NULL,
 PRIMARY KEY(issue_id,revision),
 FOREIGN KEY(issue_id,revision) REFERENCES issue_control_revisions(issue_id,revision) DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE TRIGGER issue_progress_immutable_update BEFORE UPDATE ON issue_progress_revisions
BEGIN SELECT RAISE(ABORT,'issue progress revisions are immutable'); END;
CREATE TRIGGER issue_progress_immutable_delete BEFORE DELETE ON issue_progress_revisions
BEGIN SELECT RAISE(ABORT,'issue progress revisions are immutable'); END;

-- An assessment advances the aggregate revision without changing source context.
-- Its immutable row is inserted first; the deferred snapshot reference must be
-- satisfied by the aggregate save before this transaction can commit.
DROP TRIGGER context_item_update;
CREATE TRIGGER context_item_update AFTER UPDATE OF revision ON work_items
WHEN NOT EXISTS(SELECT 1 FROM issue_progress_revisions WHERE issue_id=NEW.work_item_id AND revision=NEW.revision)
BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=NEW.work_item_id;
END;
