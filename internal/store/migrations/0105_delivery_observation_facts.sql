-- Current PR facts have one identity. Retain original Issue snapshots and
-- request ownership before removing them from mutable query metadata.
CREATE TABLE retained_delivery_observations (
 work_item_id TEXT NOT NULL,
 delivery_id TEXT NOT NULL,
 generation INTEGER NOT NULL,
 nonce TEXT NOT NULL,
 host_id TEXT NOT NULL,
 instance_id TEXT NOT NULL,
 query_until INTEGER NOT NULL,
 next_check_at INTEGER NOT NULL,
 refresh_requested INTEGER NOT NULL,
 server_refresh_revision INTEGER NOT NULL,
 observation_json TEXT NOT NULL,
 PRIMARY KEY(work_item_id,delivery_id)
) STRICT;
INSERT INTO retained_delivery_observations
 SELECT work_item_id,delivery_id,generation,nonce,host_id,instance_id,
 query_until,next_check_at,refresh_requested,server_refresh_revision,observation_json
 FROM delivery_observations;

ALTER TABLE server_delivery_jobs ADD COLUMN fact_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_delivery_jobs ADD COLUMN changed_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_delivery_queries ADD COLUMN baseline_upgrade_id TEXT NOT NULL DEFAULT '';
ALTER TABLE server_delivery_queries ADD COLUMN baseline_upgrade_revision INTEGER NOT NULL DEFAULT 0;
CREATE INDEX server_delivery_queries_target_started ON server_delivery_queries(provider,repository,number,started_at);

-- Recover an in-flight query's baseline only from its matching original
-- members. Contradictory or missing members cannot invent a current baseline.
WITH baselines AS (
 SELECT m.query_id,MIN(COALESCE(json_extract(o.observation_json,'$.baseline_upgrade_id'),'')) baseline_id,
 MIN(COALESCE(json_extract(o.observation_json,'$.baseline_upgrade_revision'),0)) baseline_revision,
 COUNT(DISTINCT json_array(COALESCE(json_extract(o.observation_json,'$.baseline_upgrade_id'),''),COALESCE(json_extract(o.observation_json,'$.baseline_upgrade_revision'),0))) variants
 FROM server_delivery_members m JOIN retained_delivery_observations o
 ON o.work_item_id=m.issue_id AND o.delivery_id=m.delivery_id
 WHERE o.nonce=m.query_id AND o.generation=m.generation
 GROUP BY m.query_id
)
UPDATE server_delivery_queries SET
 baseline_upgrade_id=(SELECT baseline_id FROM baselines WHERE query_id=server_delivery_queries.query_id),
 baseline_upgrade_revision=(SELECT baseline_revision FROM baselines WHERE query_id=server_delivery_queries.query_id)
 WHERE query_id IN (SELECT query_id FROM baselines WHERE variants=1);

-- All admitted observers now use this same fact. A newer legacy runner result
-- must supersede an older server cache; equal timestamps retain the canonical
-- result. Retired/late reports are not sources of current provider state.
WITH candidates AS (
 SELECT provider,repository,number,observation_json,0 priority,'' issue_id,'' delivery_id
 FROM server_delivery_jobs
 UNION ALL
 SELECT r.provider,r.repository,r.number,
 CASE WHEN o.nonce<>'' THEN json_remove(o.observation_json,'$.baseline_upgrade_id','$.baseline_upgrade_revision') ELSE o.observation_json END,
 1,o.work_item_id,o.delivery_id
 FROM retained_delivery_observations o JOIN server_delivery_references r
 ON r.issue_id=o.work_item_id AND r.delivery_id=o.delivery_id
), ranked AS (
 SELECT *,ROW_NUMBER() OVER (PARTITION BY provider,repository,number
 ORDER BY MAX(COALESCE(json_extract(observation_json,'$.attempted_at'),0),COALESCE(json_extract(observation_json,'$.checked_at'),0)) DESC,
 priority,COALESCE(json_extract(observation_json,'$.error_kind'),'')<>'' DESC,issue_id,delivery_id) ordinal
 FROM candidates
 WHERE COALESCE(json_extract(observation_json,'$.observer'),'')<>''
 OR COALESCE(json_extract(observation_json,'$.state'),'')<>''
 OR COALESCE(json_extract(observation_json,'$.error_kind'),'')<>''
 OR COALESCE(json_extract(observation_json,'$.checked_at'),0)>0
 OR COALESCE(json_extract(observation_json,'$.attempted_at'),0)>0
) , snapshots AS (
 SELECT *,ROW_NUMBER() OVER (PARTITION BY provider,repository,number
 ORDER BY COALESCE(json_extract(observation_json,'$.checked_at'),0) DESC,priority,issue_id,delivery_id) snapshot_ordinal
 FROM candidates
 WHERE COALESCE(json_extract(observation_json,'$.checked_at'),0)>0
 AND json_extract(observation_json,'$.state') IN ('open','merged','closed')
)
UPDATE server_delivery_jobs SET observation_json=(
 -- These are exactly protocol.DeliverySnapshot's fields, plus its successful
 -- checked_at. The latest attempt/error and its actual query baseline remain
 -- from the selected attempt; a failed older copy cannot roll known facts back.
 SELECT CASE WHEN COALESCE(json_extract(s.observation_json,'$.checked_at'),0)>COALESCE(json_extract(a.observation_json,'$.checked_at'),0)
 THEN json_patch(json_remove(a.observation_json,'$.ci','$.merged_by','$.closed_by'),json_object(
  'lineage_error_kind',json_extract(s.observation_json,'$.lineage_error_kind'),
  'mergeability',json_extract(s.observation_json,'$.mergeability'),
  'ci',json_extract(s.observation_json,'$.ci'),
  'target_sha',json_extract(s.observation_json,'$.target_sha'),
  'target_contains_merge',json(CASE json_type(s.observation_json,'$.target_contains_merge') WHEN 'true' THEN 'true' WHEN 'false' THEN 'false' ELSE 'null' END),
  'base_sha',json_extract(s.observation_json,'$.base_sha'),
  'state',json_extract(s.observation_json,'$.state'),
  'head_sha',json_extract(s.observation_json,'$.head_sha'),
  'merge_sha',json_extract(s.observation_json,'$.merge_sha'),
  'url',json_extract(s.observation_json,'$.url'),
  'provider_updated_at',json_extract(s.observation_json,'$.provider_updated_at'),
  'merged_at',json_extract(s.observation_json,'$.merged_at'),
  'closed_at',json_extract(s.observation_json,'$.closed_at'),
  'merged_by',json_extract(s.observation_json,'$.merged_by'),
  'closed_by',json_extract(s.observation_json,'$.closed_by'),
  'checked_at',json_extract(s.observation_json,'$.checked_at')
 )) ELSE a.observation_json END
 FROM ranked a LEFT JOIN snapshots s
 ON s.provider=a.provider AND s.repository=a.repository AND s.number=a.number AND s.snapshot_ordinal=1
 WHERE a.ordinal=1 AND a.provider=server_delivery_jobs.provider AND a.repository=server_delivery_jobs.repository AND a.number=server_delivery_jobs.number)
 WHERE EXISTS(SELECT 1 FROM ranked WHERE ordinal=1 AND provider=server_delivery_jobs.provider AND repository=server_delivery_jobs.repository AND number=server_delivery_jobs.number);

UPDATE server_delivery_jobs SET observation_json=json_remove(observation_json,'$.work_item_id','$.delivery_id','$.generation','$.refresh_pending'),
 fact_revision=CASE WHEN COALESCE(json_extract(observation_json,'$.observer'),'')<>''
 OR COALESCE(json_extract(observation_json,'$.state'),'')<>''
 OR COALESCE(json_extract(observation_json,'$.error_kind'),'')<>''
 OR COALESCE(json_extract(observation_json,'$.checked_at'),0)>0
 OR COALESCE(json_extract(observation_json,'$.attempted_at'),0)>0 THEN 1 ELSE 0 END;
UPDATE server_delivery_jobs SET changed_revision=fact_revision;

ALTER TABLE delivery_observations RENAME COLUMN observation_json TO query_json;
UPDATE delivery_observations SET query_json=json_object(
 'baseline_upgrade_id',COALESCE(json_extract(query_json,'$.baseline_upgrade_id'),''),
 'baseline_upgrade_revision',COALESCE(json_extract(query_json,'$.baseline_upgrade_revision'),0),
 -- Equal wall-clock times cannot establish that this member saw the selected
 -- fact. Existing bounded fanout consumes the initial revision once.
 'observed_revision',0);
