CREATE TABLE external_recoveries (
 instance_id TEXT NOT NULL,
 recovery_id TEXT NOT NULL,
 receipt_json TEXT NOT NULL,
 upgrade_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(instance_id,recovery_id)
);
