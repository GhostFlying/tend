-- A stopped wait can contribute to the same Inbox entry as an overlapping
-- wait for the same candidate. This link has no scheduling semantics.
ALTER TABLE run_external_waits ADD COLUMN inbox_attention_id TEXT REFERENCES attentions(attention_id);
CREATE INDEX external_waits_inbox_episode ON run_external_waits(
 json_extract(body_json,'$.issue_id'), json_extract(body_json,'$.target_id'),
 json_extract(body_json,'$.observed_head')
) WHERE status='waiting' AND inbox_attention_id IS NOT NULL;
