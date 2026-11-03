package protocol

// ArtifactShare grants only one immutable result to one responsibility epoch.
type ArtifactShare struct {
	ShareID         string         `json:"share_id"`
	Revision        int64          `json:"revision"`
	IssueID         string         `json:"issue_id"`
	AssignmentEpoch int64          `json:"assignment_epoch"`
	ArtifactID      string         `json:"artifact_id"`
	SourceTaskID    string         `json:"source_task_id"`
	SourceIssueID   string         `json:"source_issue_id"`
	Digest          string         `json:"digest"`
	RefSHA256       string         `json:"ref_sha256"`
	Files           []ArtifactFile `json:"files"`
	Status          string         `json:"status"`
	Applicable      bool           `json:"applicable"`
	Reason          string         `json:"reason"`
	SourceMessageID string         `json:"source_message_id,omitempty"`
	SourceRevision  *int64         `json:"source_revision,omitempty"`
	CreatedAt       int64          `json:"created_at"`
	UpdatedAt       int64          `json:"updated_at"`
}

type GrantArtifactSharePayload struct {
	IssueControlPayload
	ArtifactID string `json:"artifact_id"`
	RefSHA256  string `json:"ref_sha256"`
}

type RevokeArtifactSharePayload struct {
	IssueControlPayload
	ShareID               string `json:"share_id"`
	ExpectedShareRevision int64  `json:"expected_share_revision"`
}

type ArtifactSharePage struct {
	Items []ArtifactShare `json:"items"`
	Next  string          `json:"next,omitempty"`
}

const (
	OpGrantArtifactShare         = "artifact_shares.grant"
	OpRevokeArtifactShare        = "artifact_shares.revoke"
	PrefixArtifactShare   Prefix = "shr"
)
