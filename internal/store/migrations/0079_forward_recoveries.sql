-- Forward recovery appends to terminal upgrade history; it never edits a batch.
CREATE TABLE server_forward_recoveries (
 recovery_id TEXT PRIMARY KEY,
 original_upgrade_id TEXT NOT NULL REFERENCES upgrades(upgrade_id),
 request_id TEXT NOT NULL UNIQUE,
 state_json TEXT NOT NULL,
 linked INTEGER NOT NULL DEFAULT 0 CHECK (linked IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX server_forward_recoveries_original ON server_forward_recoveries(original_upgrade_id);
