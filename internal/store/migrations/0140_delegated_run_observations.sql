-- Frozen Run ownership survives a missed heartbeat. Contact and progress
-- watermarks describe the server's knowledge, independent of execution.
ALTER TABLE runs ADD COLUMN announced_event_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN stop_requested_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN runner_stopped_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN writer_state_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE manager_native_events ADD COLUMN received_at INTEGER NOT NULL DEFAULT 0;

CREATE TRIGGER run_stop_requested AFTER UPDATE OF desired_state ON runs
WHEN NEW.desired_state='stopped' AND OLD.desired_state<>'stopped'
BEGIN
 UPDATE runs SET stop_requested_at=NEW.updated_at WHERE run_id=NEW.run_id AND stop_requested_at=0;
END;
CREATE TRIGGER action_stop_requested AFTER UPDATE OF status ON plan_actions
WHEN NEW.status IN ('cancelled','superseded') AND NEW.status<>OLD.status
BEGIN
 UPDATE runs SET stop_requested_at=NEW.updated_at WHERE action_id=NEW.action_id AND stop_requested_at=0 AND observed_state NOT IN ('finished','failed');
END;
CREATE TRIGGER task_stop_requested AFTER UPDATE OF lifecycle ON tasks
WHEN NEW.lifecycle='cancelled' AND NEW.lifecycle<>OLD.lifecycle
BEGIN
 UPDATE runs SET stop_requested_at=NEW.updated_at WHERE task_id=NEW.task_id AND stop_requested_at=0 AND observed_state NOT IN ('finished','failed');
END;
