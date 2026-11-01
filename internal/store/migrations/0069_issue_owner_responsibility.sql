-- Policies apply to new, explicitly published responsibility contracts only.
-- Existing Issue, task and run histories retain their original semantics.
CREATE TABLE issue_ownership_policies (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 policy TEXT NOT NULL CHECK(policy='issue-owner-v1'),
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE task_owner_contracts (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL,
 agent_ref TEXT NOT NULL,
 agent_revision INTEGER NOT NULL,
 runtime_id TEXT NOT NULL,
 runtime_revision INTEGER NOT NULL,
 model TEXT NOT NULL,
 effort TEXT NOT NULL,
 review_issue_id TEXT NOT NULL UNIQUE REFERENCES issue_controls(issue_id),
 review_plan_json TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE run_issue_responsibilities (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL,
 issue_revision INTEGER NOT NULL,
 agent_ref TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
