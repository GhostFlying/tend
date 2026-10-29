-- Attention reminders use the existing rule/occurrence engine even without an
-- Issue. Preserve every existing value, history row and FK with FK checks on.
CREATE TEMP TABLE attention_clock_followup_rules AS SELECT * FROM followup_rules;
CREATE TEMP TABLE attention_clock_followup_revisions AS SELECT * FROM followup_revisions;
CREATE TEMP TABLE attention_clock_followup_targets AS SELECT * FROM followup_targets;
CREATE TEMP TABLE attention_clock_followup_occurrences AS SELECT * FROM followup_occurrences;
CREATE TEMP TABLE attention_clock_followup_registrations AS SELECT * FROM followup_registrations;
DROP TABLE followup_revisions;
DROP TABLE followup_targets;
DROP TABLE followup_occurrences;
DROP TABLE followup_registrations;
DROP TABLE followup_rules;

CREATE TABLE followup_rules (
 rule_id TEXT PRIMARY KEY,
 work_item_id TEXT REFERENCES work_items(work_item_id),
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
 work_item_id TEXT REFERENCES work_items(work_item_id),
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

CREATE INDEX followup_occurrences_latest ON followup_occurrences(rule_id,revision DESC);
INSERT INTO followup_rules SELECT * FROM attention_clock_followup_rules;
INSERT INTO followup_revisions SELECT * FROM attention_clock_followup_revisions;
INSERT INTO followup_targets SELECT * FROM attention_clock_followup_targets;
INSERT INTO followup_occurrences SELECT * FROM attention_clock_followup_occurrences;
INSERT INTO followup_registrations SELECT * FROM attention_clock_followup_registrations;
DROP TABLE attention_clock_followup_rules;
DROP TABLE attention_clock_followup_revisions;
DROP TABLE attention_clock_followup_targets;
DROP TABLE attention_clock_followup_occurrences;
DROP TABLE attention_clock_followup_registrations;
