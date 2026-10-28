-- No historical responsibility backfill: only canonical new creation adopts rows.
CREATE TABLE issue_controls (
 issue_id TEXT PRIMARY KEY REFERENCES work_items(work_item_id),
 parent_issue_id TEXT REFERENCES issue_controls(issue_id),
 state TEXT NOT NULL CHECK(state IN ('active','paused','cancelling','cancelled','closed')),
 assignee_kind TEXT NOT NULL CHECK(assignee_kind IN ('owner','agent')),
 assignee_ref TEXT NOT NULL CHECK(length(trim(assignee_ref))>0 AND assignee_ref=trim(assignee_ref)),
 assignment_epoch INTEGER NOT NULL CHECK(assignment_epoch>0),
 assignment_state TEXT NOT NULL CHECK(assignment_state IN ('active','handoff')),
 pending_assignee_kind TEXT CHECK(pending_assignee_kind IN ('owner','agent')),
 pending_assignee_ref TEXT,
 pending_handoff_id TEXT,
 updated_at INTEGER NOT NULL,
 FOREIGN KEY(issue_id,pending_handoff_id) REFERENCES issue_assignment_handoffs(issue_id,handoff_id),
 CHECK(parent_issue_id IS NULL OR parent_issue_id<>issue_id),
 CHECK((assignment_state='active' AND pending_assignee_kind IS NULL AND pending_assignee_ref IS NULL AND pending_handoff_id IS NULL)
 OR (assignment_state='handoff' AND pending_assignee_kind IS NOT NULL AND pending_assignee_ref IS NOT NULL AND length(trim(pending_assignee_ref))>0 AND pending_handoff_id IS NOT NULL))
) STRICT;
CREATE INDEX issue_controls_parent ON issue_controls(parent_issue_id);
CREATE INDEX issue_controls_assignee ON issue_controls(assignee_kind,assignee_ref,state);

CREATE TABLE issue_dependencies (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 depends_on TEXT NOT NULL REFERENCES issue_controls(issue_id),
 PRIMARY KEY(issue_id,depends_on),
 CHECK(issue_id<>depends_on)
) STRICT;

CREATE TABLE issue_control_revisions (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 revision INTEGER NOT NULL CHECK(revision>0),
 snapshot_json TEXT NOT NULL,
 reason_json TEXT NOT NULL,
 actor_json TEXT NOT NULL,
 request_id TEXT NOT NULL,
 source_message_id TEXT,
 coordination_execution_id TEXT,
 effect_seq INTEGER,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(issue_id,revision),
 FOREIGN KEY(issue_id,revision) REFERENCES work_item_revisions(work_item_id,revision),
 CHECK((coordination_execution_id IS NULL AND effect_seq IS NULL)
 OR (coordination_execution_id IS NOT NULL AND effect_seq IS NOT NULL AND effect_seq>0))
) STRICT;
CREATE TRIGGER issue_control_revision_immutable_update BEFORE UPDATE ON issue_control_revisions
BEGIN SELECT RAISE(ABORT,'issue control revisions are immutable'); END;
CREATE TRIGGER issue_control_revision_immutable_delete BEFORE DELETE ON issue_control_revisions
BEGIN SELECT RAISE(ABORT,'issue control revisions are immutable'); END;

CREATE TABLE issue_assignment_handoffs (
 handoff_id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 from_epoch INTEGER NOT NULL CHECK(from_epoch>0),
 to_epoch INTEGER NOT NULL CHECK(to_epoch=from_epoch+1),
 from_assignee_json TEXT NOT NULL,
 target_assignee_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('fenced','reconciling','ready','completed','blocked')),
 obligations_json TEXT NOT NULL,
 reconciliation_json TEXT NOT NULL DEFAULT '',
 source_message_id TEXT,
 request_id TEXT NOT NULL,
 reason TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(issue_id,to_epoch),
 UNIQUE(issue_id,handoff_id)
) STRICT;
CREATE UNIQUE INDEX issue_handoff_pending ON issue_assignment_handoffs(issue_id) WHERE status<>'completed';
