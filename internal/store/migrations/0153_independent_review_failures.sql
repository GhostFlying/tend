CREATE TABLE independent_review_failures (
    run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
    task_id TEXT NOT NULL REFERENCES tasks(task_id),
    role TEXT NOT NULL CHECK(role IN ('implement', 'accept')),
    artifact_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    root_run_id TEXT NOT NULL REFERENCES runs(run_id),
    retries INTEGER NOT NULL CHECK(retries BETWEEN 0 AND 3),
    retry_action_id TEXT UNIQUE REFERENCES plan_actions(action_id),
    resolution_run_id TEXT REFERENCES runs(run_id)
) STRICT;
CREATE INDEX independent_review_failure_scope ON independent_review_failures(task_id, role, artifact_id);
CREATE TABLE independent_review_run_targets (
    run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
    target_key TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL
) STRICT;
CREATE INDEX independent_review_run_target ON independent_review_run_targets(target_key, head_sha, base_sha);
