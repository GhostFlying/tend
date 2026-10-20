-- A task that ended waits for nothing. Cancelling used to keep the block
-- reason the task had, such as waiting_decision.
UPDATE tasks SET block_reason = 'none' WHERE lifecycle IN ('completed', 'cancelled');
