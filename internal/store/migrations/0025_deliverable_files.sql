-- Immutable file uploads outlive their execution workspace.
CREATE TABLE deliverable_files (
    file_id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(task_id),
    run_id TEXT NOT NULL REFERENCES runs(run_id),
    name TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    sha256 TEXT NOT NULL,
    content BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX deliverable_files_run ON deliverable_files(run_id);
