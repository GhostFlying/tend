-- Forced process replacement preserves unknown work independently from ordinary
-- lease fences. Older runners reject this schema before they can clear it.
CREATE TABLE bounded_recovery_quarantines (
 request_id TEXT PRIMARY KEY,
 authority_json TEXT NOT NULL,
 snapshot_json TEXT NOT NULL,
 active INTEGER NOT NULL CHECK(active IN (0,1)),
 superseded_by TEXT,
 admission_release_json TEXT
) STRICT;
