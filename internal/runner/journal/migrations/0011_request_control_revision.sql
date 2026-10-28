-- NULL preserves the absent CAS condition of requests recorded by older runners.
-- An explicit zero remains distinct from an absent expected revision.
ALTER TABLE requests ADD COLUMN expected_control_revision INTEGER;
