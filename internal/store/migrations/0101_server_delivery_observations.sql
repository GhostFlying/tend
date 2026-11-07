-- D185: materialized delivery identities and shared, bounded server reads.
ALTER TABLE delivery_observations ADD COLUMN server_refresh_revision INTEGER NOT NULL DEFAULT 0;
CREATE TABLE server_delivery_references (
 issue_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 delivery_id TEXT NOT NULL,
 provider TEXT NOT NULL,
 repository TEXT NOT NULL,
 number INTEGER NOT NULL,
 active INTEGER NOT NULL,
 PRIMARY KEY(issue_id,delivery_id)
) STRICT;
CREATE INDEX server_delivery_references_identity ON server_delivery_references(provider,repository,number,issue_id,delivery_id);

CREATE TABLE server_delivery_jobs (
 provider TEXT NOT NULL,
 repository TEXT NOT NULL,
 number INTEGER NOT NULL,
 next_at INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 nonce TEXT NOT NULL DEFAULT '',
 query_until INTEGER NOT NULL DEFAULT 0,
 observation_json TEXT NOT NULL DEFAULT '{}',
 refresh_revision INTEGER NOT NULL DEFAULT 0,
 observed_refresh_revision INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(provider,repository,number)
) STRICT;
CREATE INDEX server_delivery_jobs_due ON server_delivery_jobs(next_at,provider,repository,number);

CREATE TABLE server_delivery_queries (
 query_id TEXT PRIMARY KEY,
 provider TEXT NOT NULL,
 repository TEXT NOT NULL,
 number INTEGER NOT NULL,
 status TEXT NOT NULL,
 started_at INTEGER NOT NULL,
 refresh_revision INTEGER NOT NULL DEFAULT 0,
 finished_at INTEGER NOT NULL DEFAULT 0,
 result_json TEXT NOT NULL DEFAULT '{}',
 late_result_json TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE TABLE server_delivery_members (
 query_id TEXT NOT NULL REFERENCES server_delivery_queries(query_id),
 issue_id TEXT NOT NULL,
 delivery_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 PRIMARY KEY(query_id,issue_id,delivery_id)
) STRICT;
CREATE TABLE retired_runner_delivery_queries (
 nonce TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL,
 delivery_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 host_id TEXT NOT NULL,
 instance_id TEXT NOT NULL,
 query_until INTEGER NOT NULL,
 retired_at INTEGER NOT NULL,
 event_id TEXT NOT NULL DEFAULT '',
 late_report_json TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX retired_runner_delivery_identity ON retired_runner_delivery_queries(issue_id,delivery_id,query_until);

INSERT INTO server_delivery_references
 SELECT w.work_item_id,json_extract(d.value,'$.delivery_id'),json_extract(d.value,'$.provider'),
 json_extract(d.value,'$.repository'),json_extract(d.value,'$.number'),w.status<>'closed'
 FROM work_items w,json_each(w.body_json,'$.deliveries') d;
INSERT INTO server_delivery_jobs(provider,repository,number)
 SELECT DISTINCT provider,repository,number FROM server_delivery_references;

CREATE TRIGGER server_delivery_references_insert AFTER INSERT ON work_items BEGIN
 INSERT INTO server_delivery_references
 SELECT NEW.work_item_id,json_extract(d.value,'$.delivery_id'),json_extract(d.value,'$.provider'),
 json_extract(d.value,'$.repository'),json_extract(d.value,'$.number'),NEW.status<>'closed'
 FROM json_each(NEW.body_json,'$.deliveries') d;
 INSERT INTO server_delivery_jobs(provider,repository,number)
 SELECT provider,repository,number FROM server_delivery_references WHERE issue_id=NEW.work_item_id
 ON CONFLICT(provider,repository,number) DO UPDATE SET next_at=MIN(server_delivery_jobs.next_at,NEW.updated_at);
END;
CREATE TRIGGER server_delivery_references_update AFTER UPDATE OF body_json,status ON work_items
 WHEN COALESCE(json_extract(OLD.body_json,'$.deliveries'),'[]')<>COALESCE(json_extract(NEW.body_json,'$.deliveries'),'[]') OR OLD.status<>NEW.status
BEGIN
 DELETE FROM server_delivery_references WHERE issue_id=NEW.work_item_id;
 INSERT INTO server_delivery_references
 SELECT NEW.work_item_id,json_extract(d.value,'$.delivery_id'),json_extract(d.value,'$.provider'),
 json_extract(d.value,'$.repository'),json_extract(d.value,'$.number'),NEW.status<>'closed'
 FROM json_each(NEW.body_json,'$.deliveries') d;
 INSERT INTO server_delivery_jobs(provider,repository,number)
 SELECT provider,repository,number FROM server_delivery_references WHERE issue_id=NEW.work_item_id
 ON CONFLICT(provider,repository,number) DO UPDATE SET next_at=MIN(server_delivery_jobs.next_at,NEW.updated_at);
END;
