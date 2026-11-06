-- Explicit immutable evidence references belong to the original verdict.
ALTER TABLE acceptances ADD COLUMN files_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(files_json) AND json_type(files_json) = 'array');

-- Capability and feedback authority are frozen by the original claim, never
-- inferred from a later runner incarnation or a later acceptance.
CREATE TABLE run_acceptance_evidence_contracts (
    run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
    files_allowed INTEGER NOT NULL CHECK (files_allowed IN (0, 1)),
    feedback_acceptance_id TEXT NOT NULL DEFAULT ''
) STRICT;
