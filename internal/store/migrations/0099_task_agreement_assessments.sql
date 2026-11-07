-- An assessment retains the frozen specification and acknowledges only the
-- exact current source snapshot considered by the responsible Issue owner.
CREATE TABLE task_agreement_assessments (
 request_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 spec_version INTEGER NOT NULL,
 task_control_revision INTEGER NOT NULL,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL,
 assignee_ref TEXT NOT NULL,
 context_issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 frozen_digest TEXT NOT NULL,
 current_digest TEXT NOT NULL,
 source_digest TEXT NOT NULL,
 context_revision INTEGER NOT NULL,
 issue_revision INTEGER NOT NULL,
 definition_digest TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('continue_existing','updated','stopped')),
 reason TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(request_id,task_id,context_issue_id)
) STRICT;
CREATE INDEX task_agreement_assessments_current
 ON task_agreement_assessments(task_id,spec_version,context_issue_id);
CREATE TRIGGER task_agreement_assessments_immutable_update BEFORE UPDATE ON task_agreement_assessments
BEGIN SELECT RAISE(ABORT,'task agreement assessments are immutable'); END;
CREATE TRIGGER task_agreement_assessments_immutable_delete BEFORE DELETE ON task_agreement_assessments
BEGIN SELECT RAISE(ABORT,'task agreement assessments are immutable'); END;
CREATE INDEX local_hosted_coordination_effects_request ON local_hosted_coordination_effects(request_id);
CREATE INDEX runner_coordination_effects_request ON runner_coordination_effects(request_id);

-- Reuse the existing notification delay and sweep; this is a debounce buffer,
-- and due changes enter the existing frozen background-batch/outbox protocol.
CREATE TABLE issue_change_notifications (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 event_id TEXT NOT NULL,
 first_event_seq INTEGER NOT NULL,
 event_seq INTEGER NOT NULL,
 kind TEXT NOT NULL,
 issue_revision INTEGER NOT NULL,
 assignment_epoch INTEGER NOT NULL,
 last_changed_at INTEGER NOT NULL,
 due_at INTEGER NOT NULL
) STRICT;
CREATE INDEX issue_change_notifications_due ON issue_change_notifications(due_at,issue_id);
