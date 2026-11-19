-- Version the pausing phase before a writer can persist it in runs.phase.
-- No rows need rewriting. The existing journal version check fences older writers.
SELECT 1;
