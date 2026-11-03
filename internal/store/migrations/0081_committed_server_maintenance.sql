-- Independent committed-host maintenance never rewrites an upgrade or recovery.
CREATE TABLE server_committed_maintenances (
 maintenance_id TEXT PRIMARY KEY,
 request_id TEXT NOT NULL UNIQUE,
 parent_id TEXT NOT NULL DEFAULT '',
 state_json TEXT NOT NULL,
 linked INTEGER NOT NULL DEFAULT 0 CHECK (linked IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX server_committed_maintenances_parent ON server_committed_maintenances(parent_id) WHERE parent_id<>'';
