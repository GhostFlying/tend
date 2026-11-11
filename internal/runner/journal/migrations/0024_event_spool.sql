-- Queryable native terminal identity is independent from its opaque spool body.
ALTER TABLE events ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
UPDATE events SET thread_id=COALESCE(json_extract(body_json,'$.thread_id'),'') WHERE json_valid(body_json);
CREATE INDEX events_native_terminal ON events(run_id,kind,thread_id,seq DESC);
-- Confirmed native sessions are execution facts, not RPC response history.
CREATE TABLE manager_native_attachments (
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 thread_id TEXT NOT NULL,
 request_json TEXT NOT NULL,
 PRIMARY KEY(run_id,thread_id)
) STRICT;
INSERT OR IGNORE INTO manager_native_attachments(run_id,thread_id,request_json)
 SELECT run_id, COALESCE(json_extract(reply_json,'$.result.id'),json_extract(reply_json,'$.result.ID')),
 json_set(request_json,'$.method','resume','$.payload.thread_id',COALESCE(json_extract(reply_json,'$.result.id'),json_extract(reply_json,'$.result.ID')))
 FROM manager_native_operations WHERE status='completed'
 AND json_extract(request_json,'$.method') IN ('start','resume','fork')
 AND COALESCE(json_extract(reply_json,'$.error'),'')=''
 AND COALESCE(json_extract(reply_json,'$.result.id'),json_extract(reply_json,'$.result.ID'),'')<>'';
-- Pure native observations never have an external execution to reconcile.
-- Their pending transport replies remain in requests until server confirmation.
DELETE FROM manager_native_operations
 WHERE json_extract(request_json,'$.method') IN ('models','find','usage');
-- Other historical completed rows have no durable server-ACK bit in the old
-- schema. Keep their unique results until a real settlement manifest is checked;
-- completed-local and physical Stop are not a fabricated server receipt.

-- Only IDs whose result delivery was actually acknowledged remain outstanding.
-- They survive Run completion until the peer confirms this delivery watermark.
CREATE TABLE manager_receipt_ack_ids (
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 command_id TEXT NOT NULL,
 PRIMARY KEY(run_id,command_id)
) STRICT;
ALTER TABLE runs ADD COLUMN result_acknowledged INTEGER NOT NULL DEFAULT 0 CHECK(result_acknowledged IN (0,1));
