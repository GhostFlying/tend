ALTER TABLE coordination_delegation_attempts ADD COLUMN intent_json TEXT NOT NULL DEFAULT '';
ALTER TABLE coordination_delegation_attempts ADD COLUMN corrects_request_id TEXT NOT NULL DEFAULT '';
CREATE INDEX delegation_correction ON coordination_delegation_attempts(domain,corrects_request_id) WHERE succeeded=1;
