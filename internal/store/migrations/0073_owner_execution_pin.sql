-- Freeze the complete private-snapshot identity rather than revision labels alone.
ALTER TABLE task_owner_contracts ADD COLUMN execution_pin_json TEXT;
UPDATE task_owner_contracts SET execution_pin_json=(
 SELECT e.pin_json FROM run_agent_executions e JOIN runs r USING(run_id)
 WHERE r.task_id=task_owner_contracts.task_id AND r.role='implement'
 ORDER BY r.created_at,r.run_id LIMIT 1
);
