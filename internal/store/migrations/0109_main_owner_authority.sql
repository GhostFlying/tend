CREATE TABLE main_owner_authorities (
 execution_id TEXT PRIMARY KEY REFERENCES main_turns(request_id),
 owner_subject TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;

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
