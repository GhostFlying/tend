-- The grouped display column includes both doing and review. Ungrouped
-- API and CLI readers retain the exact entries from the separate projection.
CREATE TABLE issue_board_grouped_stage_entries (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 status TEXT NOT NULL CHECK(status IN ('backlog','todo','in_progress','done','cancelled')),
 entered_at INTEGER NOT NULL,
 basis TEXT NOT NULL CHECK(basis IN ('creation','transition','creation_fallback'))
) STRICT;

INSERT INTO issue_board_grouped_stage_entries(issue_id,status,entered_at,basis)
SELECT issue_id,status,entered_at,basis FROM issue_board_stage_entries
WHERE status NOT IN ('in_progress','in_review');

-- Normalize historical snapshots before detecting boundaries, so neither
-- an ordinary edit nor a doing/review transition enters the visible column.
WITH raw_history AS (
 SELECT r.issue_id,r.revision,r.created_at,
   CASE
     WHEN json_extract(r.snapshot_json,'$.control.state')='closed' THEN 'done'
     WHEN json_extract(r.snapshot_json,'$.control.state')='cancelled' THEN 'cancelled'
     WHEN json_extract(r.snapshot_json,'$.control.state') IN ('active','paused','cancelling')
       THEN COALESCE(NULLIF(json_extract(r.snapshot_json,'$.control.work_status'),''),'todo')
   END AS status
 FROM issue_control_revisions r JOIN issue_board_stage_entries b USING(issue_id)
 WHERE b.status IN ('in_progress','in_review')
), history AS (
 SELECT issue_id,revision,created_at,CASE status WHEN 'in_review' THEN 'in_progress' ELSE status END AS status
 FROM raw_history
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
)
INSERT INTO issue_board_grouped_stage_entries(issue_id,status,entered_at,basis)
SELECT b.issue_id,'in_progress',
  CASE WHEN h.revision>1 THEN h.created_at ELSE w.created_at END,
  CASE WHEN h.revision=1 THEN 'creation' WHEN h.revision>1 THEN 'transition' ELSE 'creation_fallback' END
FROM issue_board_stage_entries b JOIN work_items w ON w.work_item_id=b.issue_id
LEFT JOIN boundaries h ON h.issue_id=b.issue_id AND h.latest_boundary=1
 AND h.latest_revision=w.revision AND h.status='in_progress'
 AND (h.revision=1 OR (h.previous_revision=h.revision-1 AND h.previous_status IN ('backlog','todo','done','cancelled')))
WHERE b.status IN ('in_progress','in_review');

CREATE INDEX issue_board_grouped_stage_order ON issue_board_grouped_stage_entries(status,entered_at DESC,issue_id);

CREATE TRIGGER issue_board_grouped_entry_insert AFTER INSERT ON issue_board_stage_entries
BEGIN
 INSERT INTO issue_board_grouped_stage_entries(issue_id,status,entered_at,basis)
 VALUES(NEW.issue_id,CASE NEW.status WHEN 'in_review' THEN 'in_progress' ELSE NEW.status END,NEW.entered_at,NEW.basis);
END;

CREATE TRIGGER issue_board_grouped_entry_update AFTER UPDATE OF status ON issue_board_stage_entries
BEGIN
 UPDATE issue_board_grouped_stage_entries
 SET status=CASE NEW.status WHEN 'in_review' THEN 'in_progress' ELSE NEW.status END,
     entered_at=NEW.entered_at,basis=NEW.basis
 WHERE issue_id=NEW.issue_id
   AND status<>CASE NEW.status WHEN 'in_review' THEN 'in_progress' ELSE NEW.status END;
END;
