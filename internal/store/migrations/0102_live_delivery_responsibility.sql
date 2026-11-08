-- A live Task may present its real registered delivery before submitting its
-- final artifact. Preserve every accepted/result-based historical source.
CREATE TABLE issue_delivery_duties_next (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 acceptance_id TEXT REFERENCES acceptances(acceptance_id),
 artifact_id TEXT REFERENCES artifacts(artifact_id),
 revision INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','awaiting_owner','authorized','deferred','executing','complete','report','ended')),
 body_json TEXT NOT NULL DEFAULT '{}',
 CHECK((acceptance_id IS NOT NULL) <> (artifact_id IS NOT NULL)
 OR (acceptance_id IS NULL AND artifact_id IS NULL AND COALESCE(json_type(body_json,'$.candidate')='object',0))),
 PRIMARY KEY(issue_id,task_id)
) STRICT;
INSERT INTO issue_delivery_duties_next SELECT * FROM issue_delivery_duties;
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
