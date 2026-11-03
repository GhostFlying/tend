-- D164: explicit immutable evidence access, scoped to an assignment epoch.
CREATE TABLE artifact_shares (
 share_id TEXT PRIMARY KEY,
 issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 assignment_epoch INTEGER NOT NULL CHECK(assignment_epoch > 0),
 artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
 revision INTEGER NOT NULL CHECK(revision > 0),
 status TEXT NOT NULL CHECK(status IN ('active','revoked')),
 state_json TEXT NOT NULL,
 ref_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX artifact_shares_issue ON artifact_shares(issue_id,share_id);
CREATE INDEX artifact_shares_access ON artifact_shares(issue_id,assignment_epoch,artifact_id,status);
CREATE TABLE artifact_share_revisions (
 share_id TEXT NOT NULL REFERENCES artifact_shares(share_id),
 revision INTEGER NOT NULL,
 request_id TEXT NOT NULL,
 state_json TEXT NOT NULL,
 PRIMARY KEY(share_id,revision)
) STRICT;
