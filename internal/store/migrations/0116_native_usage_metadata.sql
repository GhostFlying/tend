-- Source purpose is optional attribution, not native measurement identity.
-- Preserve all original facts and repair only groups differing by that single
-- absent/enriched field. Contradictory nonempty purposes, counters, provenance,
-- and native content hashes remain quarantined.
UPDATE token_usage_current AS c
SET conflict=0,
    fact_seq=(SELECT f.seq FROM token_usage_facts f
      WHERE f.source=c.source AND f.request_id=c.request_id
      ORDER BY (COALESCE(json_extract(f.payload_json,'$.purpose'),'')<>'') DESC,f.seq DESC LIMIT 1)
WHERE c.conflict=1 AND (c.source,c.request_id) IN (
  SELECT f.source,f.request_id FROM token_usage_facts f
  JOIN token_usage_current current USING(source,request_id)
  WHERE current.conflict=1
  GROUP BY f.source,f.request_id
  HAVING MIN(COALESCE(json_extract(f.payload_json,'$.native_content_hash'),'')<>'')=1
     AND COUNT(DISTINCT json_remove(f.payload_json,'$.purpose'))=1
     AND COUNT(DISTINCT NULLIF(json_extract(f.payload_json,'$.purpose'),''))<=1
     AND COUNT(DISTINCT f.subject)=1 AND COUNT(DISTINCT f.role)=1
);

CREATE INDEX token_usage_session_conflicts ON token_usage_contributions(session_id,conflict);
