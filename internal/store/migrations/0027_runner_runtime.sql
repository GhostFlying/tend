-- Communication protocol and process identity are independent of workspace capabilities.
ALTER TABLE runs ADD COLUMN protocol INTEGER NOT NULL DEFAULT 0;
CREATE TABLE host_runtime (
 host_id TEXT PRIMARY KEY REFERENCES hosts(host_id),
 instance_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 runtime_json TEXT NOT NULL,
 last_heartbeat_at INTEGER NOT NULL,
 online INTEGER NOT NULL CHECK(online IN (0,1))
) STRICT;
CREATE TABLE host_retired_instances (
 host_id TEXT NOT NULL REFERENCES hosts(host_id),
 instance_id TEXT NOT NULL,
 PRIMARY KEY(host_id,instance_id)
) STRICT;
CREATE TABLE host_controls (
 host_id TEXT PRIMARY KEY REFERENCES hosts(host_id),
 revision INTEGER NOT NULL,
 control_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
