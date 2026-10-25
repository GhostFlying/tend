CREATE TABLE work_item_completion_reviews (
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 event_id TEXT NOT NULL REFERENCES events(event_id),
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 acceptance_id TEXT NOT NULL REFERENCES acceptances(acceptance_id),
 created_at INTEGER NOT NULL,
 reviewed_revision INTEGER,
 review_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(work_item_id,event_id)
) STRICT;

-- Historical completed tasks remain pending only on unsettled open plans.
INSERT INTO work_item_completion_reviews(work_item_id,event_id,task_id,acceptance_id,created_at)
SELECT DISTINCT w.work_item_id,e.event_id,t.task_id,
 (SELECT a.acceptance_id FROM acceptances a WHERE a.task_id=t.task_id AND a.artifact_id=t.current_artifact_id AND a.spec_version=t.spec_version AND a.verdict='passed' ORDER BY a.created_at DESC,a.acceptance_id DESC LIMIT 1),e.created_at
FROM work_items w JOIN json_each(w.body_json,'$.stages') s
JOIN json_each(s.value,'$.task_ids') link
JOIN tasks t ON t.task_id=link.value
JOIN events e ON e.object_id=t.task_id AND e.kind='task.completed'
WHERE w.status!='closed' AND json_extract(s.value,'$.status') IN ('active','planned')
AND t.lifecycle='completed'
AND EXISTS(SELECT 1 FROM acceptances a WHERE a.task_id=t.task_id AND a.artifact_id=t.current_artifact_id AND a.spec_version=t.spec_version AND a.verdict='passed');
