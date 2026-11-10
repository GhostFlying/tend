CREATE TABLE main_owner_authorities (
 execution_id TEXT PRIMARY KEY REFERENCES main_turns(request_id),
 owner_subject TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX request_records_main_receipts ON request_records(request_id)
WHERE domain GLOB 'main_agent:*';

CREATE TABLE main_owner_effects (
 execution_id TEXT NOT NULL,
 effect_seq INTEGER NOT NULL CHECK(effect_seq > 0),
 step INTEGER NOT NULL CHECK(step > 0),
 call_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 owner_subject TEXT NOT NULL,
 PRIMARY KEY(execution_id,effect_seq,step),
 UNIQUE(request_id)
) STRICT;

CREATE TABLE main_owner_message_authorities (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 request_id TEXT NOT NULL REFERENCES main_owner_effects(request_id),
 owner_subject TEXT NOT NULL,
 actor_json TEXT NOT NULL
) STRICT;

-- Authority and authorship are separate. An Agent's delegated owner input is
-- not a human utterance; matching a request ID alone never grants authority.
CREATE VIEW owner_authority_messages AS
SELECT m.message_id FROM conversation_messages m WHERE m.author='owner'
UNION ALL
SELECT m.message_id FROM conversation_messages m
JOIN main_owner_message_authorities a USING(message_id) WHERE m.author='agent';
