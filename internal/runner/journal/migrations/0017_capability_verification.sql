-- Persist one bounded, read-only verification before invoking an adapter. A
-- restarted reader never treats an unresolved observation as a fresh probe.
CREATE TABLE capability_verifications (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 request_id TEXT NOT NULL UNIQUE,
 request_json TEXT NOT NULL,
 status_json TEXT NOT NULL
) STRICT;
