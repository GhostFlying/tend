-- Preserve the publishing contract and original Run responsibility. An explicit
-- stopped-handoff continuation changes only the effective assignment epoch.
CREATE TABLE task_execution_responsibility_adoptions (
 request_domain TEXT NOT NULL,
 request_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 spec_version INTEGER NOT NULL,
 task_control_revision INTEGER NOT NULL,
 decision_id TEXT NOT NULL REFERENCES decisions(decision_id),
 continuation_revision INTEGER NOT NULL,
 source_run_id TEXT NOT NULL REFERENCES run_question_handoffs(run_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 from_assignment_epoch INTEGER NOT NULL,
 assignment_epoch INTEGER NOT NULL,
 agent_ref TEXT NOT NULL,
 execution_pin_json TEXT NOT NULL CHECK(json_valid(execution_pin_json)),
 reason TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(request_domain,request_id),
 UNIQUE(source_run_id,assignment_epoch)
) STRICT;
CREATE INDEX task_responsibility_adoptions_task ON task_execution_responsibility_adoptions(task_id);
CREATE TRIGGER task_responsibility_adoptions_immutable_update BEFORE UPDATE ON task_execution_responsibility_adoptions
 BEGIN SELECT RAISE(ABORT,'execution responsibility adoption is immutable'); END;
CREATE TRIGGER task_responsibility_adoptions_immutable_delete BEFORE DELETE ON task_execution_responsibility_adoptions
 BEGIN SELECT RAISE(ABORT,'execution responsibility adoption is immutable'); END;

DROP VIEW task_execution_contracts;
CREATE VIEW frozen_task_execution_contracts AS
 SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,created_at,execution_pin_json FROM task_owner_contracts
 UNION ALL
 SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,'' AS review_issue_id,'{}' AS review_plan_json,created_at,execution_pin_json FROM creator_task_owner_contracts;
CREATE VIEW task_execution_contracts AS
 SELECT f.task_id,f.issue_id,COALESCE(a.assignment_epoch,f.assignment_epoch) AS assignment_epoch,
 f.agent_ref,f.agent_revision,f.runtime_id,f.runtime_revision,f.model,f.effort,
 f.review_issue_id,f.review_plan_json,f.created_at,f.execution_pin_json
 FROM frozen_task_execution_contracts f
 LEFT JOIN task_execution_responsibility_adoptions a ON a.rowid=(
  SELECT latest.rowid FROM task_execution_responsibility_adoptions latest
  WHERE latest.task_id=f.task_id ORDER BY latest.rowid DESC LIMIT 1);
