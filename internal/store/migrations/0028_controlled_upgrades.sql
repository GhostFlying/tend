CREATE TABLE upgrades (
 upgrade_id TEXT PRIMARY KEY,
 release_id TEXT NOT NULL,
 revision INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('running','attention','completed','cancelled')),
 phase TEXT NOT NULL,
 state_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX upgrades_unfinished ON upgrades((1)) WHERE status IN ('running','attention');
