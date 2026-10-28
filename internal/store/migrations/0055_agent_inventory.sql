-- Categories classify responsibilities; they do not grant execution roles.
CREATE TABLE agent_category_versions (
 category_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 title TEXT NOT NULL,
 description TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','retired')),
 request_id TEXT NOT NULL,
 PRIMARY KEY(category_id,revision)
) STRICT;

-- Safe projections are immutable source facts. Registration is not availability.
-- The latest server_revision is current; request_records is the atomic receipt.
CREATE TABLE agent_inventory_versions (
 host_id TEXT NOT NULL REFERENCES hosts(host_id),
 server_revision INTEGER NOT NULL CHECK(server_revision>0),
 local_revision INTEGER NOT NULL CHECK(local_revision>0),
 snapshot_id TEXT NOT NULL,
 public_sha256 TEXT NOT NULL,
 projection_json TEXT NOT NULL,
 receipt_json TEXT NOT NULL,
 instance_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 PRIMARY KEY(host_id,server_revision),
 UNIQUE(host_id,local_revision)
) STRICT;
