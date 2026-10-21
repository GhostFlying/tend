-- Whether the owner holds the run's agent (D67): set once the runner
-- learns of a takeover, cleared when the next worker has been handed the
-- run back.
ALTER TABLE runs ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
