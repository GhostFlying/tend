-- Archive transport is independent from execution and survives completed Runs.
ALTER TABLE runs ADD COLUMN archive_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN archive_uploaded_generation INTEGER NOT NULL DEFAULT 0;
CREATE INDEX runs_archive_pending ON runs(run_id)
 WHERE archive_generation > archive_uploaded_generation;
-- Reconcile registered originals once, including already completed executions.
UPDATE runs SET archive_generation=1 WHERE COALESCE(json_extract(assignment_json,'$.protocol'),0)>=3 AND EXISTS
 (SELECT 1 FROM rollout_sources s WHERE s.run_id=runs.run_id);
