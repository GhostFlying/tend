-- Advance only past a contiguous sequence of original worker events. Out-of-order
-- chunks leave a visible gap through the last sequence advertised or received.
ALTER TABLE runs ADD COLUMN received_event_sequence INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER run_events_received AFTER INSERT ON run_logs
WHEN NEW.seq=(SELECT received_event_sequence+1 FROM runs WHERE run_id=NEW.run_id AND protocol>=7)
BEGIN
 UPDATE runs SET received_event_sequence=(
   SELECT e.seq FROM run_logs e WHERE e.run_id=NEW.run_id AND e.seq>=NEW.seq
   AND NOT EXISTS(SELECT 1 FROM run_logs n WHERE n.run_id=e.run_id AND n.seq=e.seq+1)
   ORDER BY e.seq LIMIT 1
 ) WHERE run_id=NEW.run_id;
END;
