DROP TRIGGER task_acceptance_policies_immutable;

CREATE TABLE task_review_choices (
    task_id TEXT NOT NULL REFERENCES tasks(task_id),
    control_revision INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    previous_policy_json TEXT,
    policy_json TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(task_id, control_revision)
);
