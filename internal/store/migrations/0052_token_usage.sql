-- Request facts are append-only; the current contribution points at one fact.
CREATE TABLE token_usage_facts (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 source TEXT NOT NULL, request_id TEXT NOT NULL, revision INTEGER NOT NULL,
 payload_hash TEXT NOT NULL, payload_json TEXT NOT NULL,
 subject TEXT NOT NULL, role TEXT NOT NULL, received_at INTEGER NOT NULL,
 UNIQUE(source,request_id,revision,payload_hash)
);
CREATE TABLE token_usage_current (
 source TEXT NOT NULL,request_id TEXT NOT NULL,
 fact_seq INTEGER NOT NULL REFERENCES token_usage_facts(seq),
 conflict INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(source,request_id)
);
