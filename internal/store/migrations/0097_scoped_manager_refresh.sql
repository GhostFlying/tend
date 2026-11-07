CREATE INDEX issue_manager_tasks_refresh ON issue_manager_tasks(issue_id,task_id) WHERE state IN ('pending','assigned','retiring');
