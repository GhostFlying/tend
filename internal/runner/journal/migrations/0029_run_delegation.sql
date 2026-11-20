-- The assignment remains immutable. Delivery of local startup observations and
-- the latest control actually received survive loss of the server connection.
CREATE TABLE run_start_reports (
  run_id TEXT NOT NULL REFERENCES runs(run_id),
  operation TEXT NOT NULL,
  request_id TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  PRIMARY KEY (run_id, operation)
) STRICT;
ALTER TABLE runs ADD COLUMN received_desired TEXT NOT NULL DEFAULT '';
