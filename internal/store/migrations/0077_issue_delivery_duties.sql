-- Task completion is not a responsibility endpoint. No historical approvals or
-- execution are inferred; historical accepted tasks require explicit review.
CREATE TABLE issue_delivery_duties (
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 acceptance_id TEXT NOT NULL REFERENCES acceptances(acceptance_id),
 revision INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','awaiting_owner','deferred','executing','complete','report','ended')),
 body_json TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(issue_id,task_id)
) STRICT;
CREATE TABLE issue_delivery_executions (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 issue_id TEXT NOT NULL,
 source_task_id TEXT NOT NULL,
 duty_revision INTEGER NOT NULL,
 candidate_json TEXT NOT NULL,
 upgrade_id TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(issue_id,source_task_id) REFERENCES issue_delivery_duties(issue_id,task_id)
) STRICT;
INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,acceptance_id)
 SELECT o.work_item_id,t.task_id,e.event_id,
 (SELECT a.acceptance_id FROM acceptances a WHERE a.task_id=t.task_id AND a.artifact_id=t.current_artifact_id AND a.spec_version=t.spec_version AND a.verdict='passed' ORDER BY a.created_at DESC,a.acceptance_id DESC LIMIT 1)
 FROM task_work_item_owners o JOIN issue_controls c ON c.issue_id=o.work_item_id
 JOIN tasks t USING(task_id) JOIN events e ON e.object_id=t.task_id AND e.kind='task.completed'
 WHERE t.kind<>'issue_manager' AND t.lifecycle='completed'
 AND EXISTS(SELECT 1 FROM acceptances a WHERE a.task_id=t.task_id AND a.artifact_id=t.current_artifact_id AND a.spec_version=t.spec_version AND a.verdict='passed')
 ON CONFLICT DO NOTHING;
CREATE TABLE issue_delivery_answers (
 decision_id TEXT PRIMARY KEY REFERENCES decisions(decision_id),
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 content_revision INTEGER NOT NULL
) STRICT;
