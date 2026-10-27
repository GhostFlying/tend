-- Native invocation intent precedes the side effect. An unresolved invocation
-- is reported unknown after restart and is never invoked again.
CREATE TABLE run_message_deliveries (
 message_id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL,
 claim_json TEXT NOT NULL,
 invoked INTEGER NOT NULL DEFAULT 0,
 report_json TEXT NOT NULL DEFAULT '',
 settled INTEGER NOT NULL DEFAULT 0
) STRICT;
