-- Retain an optional fingerprint of the origin used by the first Git mount.
ALTER TABLE workspace_mounts ADD COLUMN origin_identity TEXT NOT NULL DEFAULT '';
