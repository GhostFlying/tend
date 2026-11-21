-- Look up waiting owner decisions by the task already linked to the input.
CREATE INDEX decisions_open_by_task ON decisions(task_id) WHERE status='open';
