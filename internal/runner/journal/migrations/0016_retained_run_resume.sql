-- Strict continuation permission is local metadata, independent of the frozen
-- assignment and saved native thread. Restart cannot silently lose the mode.
CREATE TABLE retained_run_resumes (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 authority_json TEXT NOT NULL
) STRICT;
