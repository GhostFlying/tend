-- Re-evaluate retained requests with the shared case-insensitive price resolver.
-- Only projections change; original model names and native facts stay intact.
INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current
WHERE true ON CONFLICT(source,request_id) DO NOTHING;
