-- Ratings (D60) replace the integer priority, which is no longer read or
-- written. An empty rating means the task is unrated and queues as p2.
ALTER TABLE tasks ADD COLUMN rating TEXT NOT NULL DEFAULT ''
  CHECK (rating IN ('', 'p0', 'p1', 'p2', 'p3'));
UPDATE tasks SET rating = CASE WHEN priority > 0 THEN 'p1' WHEN priority < 0 THEN 'p3' ELSE 'p2' END;
