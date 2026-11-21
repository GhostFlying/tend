ALTER TABLE workspaces ADD COLUMN cleanup_journal_id TEXT;

-- Bind older reservations where only one registered generation could have
-- received them. Keep ambiguous reservations addressable by the original
-- cleanup ID, but do not offer them to another executor.
UPDATE workspaces AS w
SET cleanup_journal_id = (
  SELECT COALESCE(json_extract(h.runtime_json, '$.journal_id'), '')
  FROM host_runtime h WHERE h.host_id=w.host_id
)
WHERE status='removing'
AND NOT EXISTS (SELECT 1 FROM draining_runners d WHERE d.host_id=w.host_id);
