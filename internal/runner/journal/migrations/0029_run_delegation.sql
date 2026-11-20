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

-- One continuation chosen by the owner of a native turn. A start whose
-- receipt is lost stays starting under its original input identity.
CREATE TABLE manager_local_turns (
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 thread_id TEXT NOT NULL, previous_turn TEXT NOT NULL, root_turn TEXT NOT NULL,
 call_id TEXT NOT NULL, next_input TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','starting','started')),
 native_turn TEXT,
 PRIMARY KEY(run_id,thread_id,previous_turn),
 UNIQUE(run_id,thread_id,native_turn)
) STRICT;
