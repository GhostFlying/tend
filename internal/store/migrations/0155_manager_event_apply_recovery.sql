-- Only failed application heads need recovery state. The original event row
-- remains the source of its body and proof of application.
CREATE TABLE manager_event_apply_failures (
 run_id TEXT PRIMARY KEY,
 seq INTEGER NOT NULL,
 attempts INTEGER NOT NULL CHECK(attempts>0),
 retry_at INTEGER NOT NULL,
 failure_code TEXT NOT NULL,
 FOREIGN KEY(run_id,seq) REFERENCES manager_native_events(run_id,seq)
) STRICT;
CREATE INDEX manager_event_apply_retry ON manager_event_apply_failures(retry_at) WHERE retry_at>0;

-- A later stop or tool-result receipt can satisfy a parked original event.
-- The original event will still be rechecked before applying anything.
CREATE TRIGGER wake_manager_event_recovery_on_stop
 AFTER UPDATE OF native_closed_at, native_close_activity ON runs
 WHEN NEW.native_closed_at>0 AND NEW.native_close_activity=NEW.native_activity
  AND (OLD.native_closed_at<>NEW.native_closed_at OR OLD.native_close_activity<>NEW.native_close_activity)
 BEGIN
  DELETE FROM manager_event_apply_failures WHERE run_id=NEW.run_id AND failure_code='original_receipt_required';
 END;
CREATE TRIGGER wake_manager_event_recovery_on_tool_result
 AFTER UPDATE OF native_delivery_status ON conversation_tool_uses
 WHEN OLD.native_delivery_status<>NEW.native_delivery_status
  AND NEW.native_delivery_status IN ('delivered','rejected','rejected_current_task')
 BEGIN
  DELETE FROM manager_event_apply_failures
  WHERE run_id=NEW.native_run_id AND failure_code='original_receipt_required'
   AND EXISTS(SELECT 1 FROM manager_native_events e WHERE e.run_id=manager_event_apply_failures.run_id AND e.seq=manager_event_apply_failures.seq
    AND json_extract(e.body_json,'$.call.ThreadID')=NEW.thread_id
    AND json_extract(e.body_json,'$.call.TurnID')=NEW.turn_id
    AND json_extract(e.body_json,'$.call.CallID')=NEW.item_id);
 END;
