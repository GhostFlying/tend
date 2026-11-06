-- Re-key existing projections into bounded fact-sequence partitions. This
-- includes unknown and future occurrence times; immutable facts stay unchanged.
INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current
WHERE true ON CONFLICT(source,request_id) DO NOTHING;
