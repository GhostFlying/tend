CREATE TABLE delivery_observations (
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 delivery_id TEXT NOT NULL,
 generation INTEGER NOT NULL DEFAULT 0,
 nonce TEXT NOT NULL DEFAULT '',
 host_id TEXT NOT NULL DEFAULT '',
 instance_id TEXT NOT NULL DEFAULT '',
 query_until INTEGER NOT NULL DEFAULT 0,
 next_check_at INTEGER NOT NULL DEFAULT 0,
 refresh_requested INTEGER NOT NULL DEFAULT 0,
 observation_json TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(work_item_id,delivery_id)
) STRICT;
