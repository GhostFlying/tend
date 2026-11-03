-- Missing rows retain the historical requirement for independent acceptance.
-- Task snapshots are written only when a new task is created.
CREATE TABLE issue_acceptance_policies (
 issue_id TEXT PRIMARY KEY REFERENCES issue_controls(issue_id),
 policy_json TEXT NOT NULL CHECK(json_valid(policy_json))
) STRICT;
CREATE TABLE task_acceptance_policies (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 policy_json TEXT NOT NULL CHECK(json_valid(policy_json))
) STRICT;
CREATE TABLE creator_task_owner_contracts (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL,
 agent_ref TEXT NOT NULL,
 agent_revision INTEGER NOT NULL,
 runtime_id TEXT NOT NULL,
 runtime_revision INTEGER NOT NULL,
 model TEXT NOT NULL,
 effort TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 execution_pin_json TEXT
) STRICT;
CREATE TRIGGER creator_task_owner_contracts_unique BEFORE INSERT ON creator_task_owner_contracts
 WHEN EXISTS(SELECT 1 FROM task_owner_contracts WHERE task_id=NEW.task_id)
 BEGIN SELECT RAISE(ABORT,'task execution contract already exists'); END;
CREATE TRIGGER legacy_task_owner_contracts_unique BEFORE INSERT ON task_owner_contracts
 WHEN EXISTS(SELECT 1 FROM creator_task_owner_contracts WHERE task_id=NEW.task_id)
 BEGIN SELECT RAISE(ABORT,'task execution contract already exists'); END;
CREATE TRIGGER task_acceptance_policies_immutable BEFORE UPDATE ON task_acceptance_policies
 BEGIN SELECT RAISE(ABORT,'task acceptance policy is immutable'); END;
CREATE VIEW task_execution_contracts AS
 SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,created_at,execution_pin_json FROM task_owner_contracts
 UNION ALL
 SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,'' AS review_issue_id,'{}' AS review_plan_json,created_at,execution_pin_json FROM creator_task_owner_contracts;

CREATE TABLE artifact_completion_reviews (
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
 created_at INTEGER NOT NULL,
 reviewed_revision INTEGER,
 review_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(work_item_id,event_id)
) STRICT;

-- Preserve delivery obligations and their child references while allowing an
-- honest current artifact result in place of a non-existent acceptance verdict.
CREATE TABLE issue_delivery_duties_next (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 acceptance_id TEXT REFERENCES acceptances(acceptance_id),
 artifact_id TEXT REFERENCES artifacts(artifact_id),
 revision INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','awaiting_owner','deferred','executing','complete','report','ended')),
 body_json TEXT NOT NULL DEFAULT '{}',
 CHECK((acceptance_id IS NOT NULL) <> (artifact_id IS NOT NULL)),
 PRIMARY KEY(issue_id,task_id)
) STRICT;
INSERT INTO issue_delivery_duties_next(issue_id,task_id,event_id,acceptance_id,revision,status,body_json)
 SELECT issue_id,task_id,event_id,acceptance_id,revision,status,body_json FROM issue_delivery_duties;
CREATE TABLE issue_delivery_executions_next (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 issue_id TEXT NOT NULL,
 source_task_id TEXT NOT NULL,
 duty_revision INTEGER NOT NULL,
 candidate_json TEXT NOT NULL,
 upgrade_id TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(issue_id,source_task_id) REFERENCES issue_delivery_duties_next(issue_id,task_id)
) STRICT;
INSERT INTO issue_delivery_executions_next SELECT * FROM issue_delivery_executions;
DROP TABLE issue_delivery_executions;
DROP TABLE issue_delivery_duties;
ALTER TABLE issue_delivery_duties_next RENAME TO issue_delivery_duties;
ALTER TABLE issue_delivery_executions_next RENAME TO issue_delivery_executions;
