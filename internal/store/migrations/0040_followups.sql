CREATE TABLE followup_rules (
 rule_id TEXT PRIMARY KEY,
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 revision INTEGER NOT NULL,
 status TEXT NOT NULL,
 next_due INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX followup_rules_due ON followup_rules(next_due,rule_id) WHERE status='active';
CREATE INDEX followup_rules_item ON followup_rules(work_item_id,rule_id);
CREATE TABLE followup_revisions (
 rule_id TEXT NOT NULL REFERENCES followup_rules(rule_id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 request_id TEXT NOT NULL,
 PRIMARY KEY(rule_id,revision)
) STRICT;
CREATE TABLE followup_targets (
 rule_id TEXT NOT NULL REFERENCES followup_rules(rule_id),
 kind TEXT NOT NULL,
 target_id TEXT NOT NULL,
 PRIMARY KEY(rule_id,kind,target_id)
) STRICT;
CREATE INDEX followup_targets_match ON followup_targets(kind,target_id,rule_id);
CREATE TABLE followup_occurrences (
 occurrence_id TEXT PRIMARY KEY,
 rule_id TEXT NOT NULL REFERENCES followup_rules(rule_id),
 revision INTEGER NOT NULL,
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 occurrence_key TEXT NOT NULL,
 message_id TEXT NOT NULL REFERENCES conversation_messages(message_id),
 status TEXT NOT NULL,
 reason TEXT NOT NULL,
 evidence TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(rule_id,occurrence_key)
) STRICT;
CREATE INDEX followup_occurrences_rule ON followup_occurrences(rule_id,occurrence_id);
CREATE INDEX followup_occurrences_message ON followup_occurrences(message_id);

CREATE TABLE followup_registrations (identity TEXT PRIMARY KEY, rule_id TEXT NOT NULL REFERENCES followup_rules(rule_id)) STRICT;
