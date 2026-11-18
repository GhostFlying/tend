-- Notification diagnosis probes the original batch's turns and precise receipts.
CREATE INDEX main_turns_message_time ON main_turns(message_id,created_at DESC);
CREATE INDEX agent_rollout_native_receipt ON agent_rollout_events(conversation_id,thread_id,turn_id)
 WHERE kind IN ('native_terminal','native_receipt');
