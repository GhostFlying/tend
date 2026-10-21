-- The harness thread a run's worker works in, recorded once the thread
-- exists, so the next attempt of the same run can continue it (D61).
ALTER TABLE runs ADD COLUMN thread_id TEXT;
