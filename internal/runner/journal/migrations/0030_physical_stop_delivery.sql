-- The physical exit fact may reach the server after business settlement.
ALTER TABLE session_stop_reports ADD COLUMN delivered INTEGER NOT NULL DEFAULT 0;
CREATE INDEX pending_physical_stop_delivery ON session_stop_reports(run_id)
 WHERE delivered=0 AND json_extract(envelope_json,'$.payload.physical_exit')=1;
