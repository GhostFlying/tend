-- This read projection is independent of Issue admission and runner priority.
CREATE TABLE issue_board_stage_entries (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 status TEXT NOT NULL CHECK(status IN ('backlog','todo','in_progress','in_review','done','cancelled')),
 entered_at INTEGER NOT NULL,
 basis TEXT NOT NULL CHECK(basis IN ('creation','transition','creation_fallback'))
) STRICT;

-- Only an uninterrupted and known suffix can establish the current entry.
-- A gap could contain both an exit and a re-entry into the same column.
WITH history AS (
 SELECT issue_id, revision, created_at,
   CASE
     WHEN json_extract(snapshot_json,'$.control.state')='closed' THEN 'done'
     WHEN json_extract(snapshot_json,'$.control.state')='cancelled' THEN 'cancelled'
     WHEN json_extract(snapshot_json,'$.control.state') IN ('active','paused','cancelling')
       THEN COALESCE(NULLIF(json_extract(snapshot_json,'$.control.work_status'),''),'todo')
   END AS status
 FROM issue_control_revisions
), ordered AS (
 SELECT *, LAG(revision) OVER history_order AS previous_revision,
     LAG(status) OVER history_order AS previous_status,
     MAX(revision) OVER (PARTITION BY issue_id) AS latest_revision
 FROM history
 WINDOW history_order AS (PARTITION BY issue_id ORDER BY revision)
), boundaries AS (
 SELECT *, ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY revision DESC) AS latest_boundary
 FROM ordered
 WHERE previous_revision IS NOT (revision-1) OR previous_status IS NOT status
), current_stages AS (
 SELECT c.issue_id, w.revision, w.created_at,
   CASE c.state WHEN 'closed' THEN 'done' WHEN 'cancelled' THEN 'cancelled'
     ELSE COALESCE(NULLIF(c.work_status,''),'todo') END AS status
 FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id
)
INSERT INTO issue_board_stage_entries(issue_id,status,entered_at,basis)
SELECT c.issue_id,c.status,
  CASE WHEN h.revision>1 THEN h.created_at ELSE c.created_at END,
  CASE WHEN h.revision=1 THEN 'creation' WHEN h.revision>1 THEN 'transition' ELSE 'creation_fallback' END
FROM current_stages c
LEFT JOIN boundaries h ON h.issue_id=c.issue_id AND h.latest_boundary=1
 AND h.latest_revision=c.revision AND h.status=c.status
 AND (h.revision=1 OR (h.previous_revision=h.revision-1 AND h.previous_status IN ('backlog','todo','in_progress','in_review','done','cancelled')));

CREATE INDEX issue_board_stage_order ON issue_board_stage_entries(status,entered_at DESC,issue_id);

-- SaveCanonicalIssue appends this revision after saving the complete control row.
-- The projection therefore observes a single effective column per transaction.
CREATE TRIGGER issue_board_record_stage_entry AFTER INSERT ON issue_control_revisions
BEGIN
 INSERT INTO issue_board_stage_entries(issue_id,status,entered_at,basis)
 SELECT c.issue_id,
   CASE c.state WHEN 'closed' THEN 'done' WHEN 'cancelled' THEN 'cancelled'
     ELSE COALESCE(NULLIF(c.work_status,''),'todo') END,
   CASE WHEN NEW.revision=1 THEN w.created_at ELSE NEW.created_at END,
   CASE WHEN NEW.revision=1 THEN 'creation' ELSE 'transition' END
 FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id
 WHERE c.issue_id=NEW.issue_id
 ON CONFLICT(issue_id) DO UPDATE SET status=excluded.status,entered_at=excluded.entered_at,basis=excluded.basis
 WHERE issue_board_stage_entries.status<>excluded.status;
END;
