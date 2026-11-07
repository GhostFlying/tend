package protocol

// IssuePRPolicy grants integration of the Issue's code, independently of deployment.
// Authority is frozen by the server from the authenticated request/source.
type IssuePRPolicy struct {
	Mode       string                  `json:"mode"`
	Repository string                  `json:"repository"`
	Reason     string                  `json:"reason"`
	Authority  *IssuePRPolicyAuthority `json:"authority,omitempty"`
}

type IssuePRPolicyAuthority struct {
	RequestID       string `json:"request_id"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	SourceRevision  int64  `json:"source_revision,omitempty"`
	ScopeDigest     string `json:"scope_digest"`
}

// IssueDeliveryGrant is a frozen authorization basis, never a synthetic owner answer.
type IssueDeliveryGrant struct {
	Mode           string `json:"mode"`
	PolicyIssueID  string `json:"policy_issue_id,omitempty"`
	PolicyRevision int64  `json:"policy_revision,omitempty"`
	PolicyDigest   string `json:"policy_digest,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
}

// SharedReviewEvidence permits reuse only when the original reviewer actually
// bound the entire PR candidate and requirement scope in its acceptance.
type SharedReviewEvidence struct {
	Provider           IssueDeliveryProvider `json:"provider"`
	Repository         string                `json:"repository"`
	Number             int64                 `json:"number"`
	HeadSHA            string                `json:"head_sha"`
	BaseSHA            string                `json:"base_sha"`
	RequirementsDigest string                `json:"requirements_digest"`
	RepositoryCheck    string                `json:"repository_check"`
}

// SharedReviewResult is submitted in the reviewer's immutable report artifact.
type SharedReviewResult struct {
	ReviewID           string                `json:"review_id"`
	HeadSHA            string                `json:"head_sha"`
	BaseSHA            string                `json:"base_sha"`
	RequirementsDigest string                `json:"requirements_digest"`
	Verdict            string                `json:"verdict"`
	RepositoryCheck    string                `json:"repository_check"`
	Findings           []SharedReviewFinding `json:"findings,omitempty"`
}

type SharedReviewFinding struct {
	ID       string `json:"id"`
	Priority string `json:"priority"`
	Body     string `json:"body"`
}

type SharedReviewDisposition struct {
	FindingID string `json:"finding_id"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
}

type SharedDeliveryReviewLink struct {
	IssueID    string `json:"issue_id"`
	DeliveryID string `json:"delivery_id"`
	TaskID     string `json:"task_id,omitempty"`
}

// SharedReviewExecutionRequirements distinguishes a frozen empty requirement
// set from historical rounds that did not record their execution requirements.
type SharedReviewExecutionRequirements struct {
	Profiles []SharedReviewExecutionProfile `json:"profiles"`
}

type SharedReviewExecutionProfile struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Exact preserves acceptance_execution's native-default effort semantics.
	Exact bool `json:"exact,omitempty"`
}

type SharedDeliveryReviewRound struct {
	ExecutionRequirements *SharedReviewExecutionRequirements `json:"execution_requirements,omitempty"`
	SupplementTaskID      string                             `json:"supplement_task_id,omitempty"`
	SupplementSpecVersion int64                              `json:"supplement_spec_version,omitempty"`
	SupplementRunID       string                             `json:"supplement_run_id,omitempty"`
	SupplementArtifactID  string                             `json:"supplement_artifact_id,omitempty"`
	SpecVersion           int64                              `json:"spec_version"`
	Number                int                                `json:"number"`
	TaskID                string                             `json:"task_id"`
	ArtifactID            string                             `json:"artifact_id,omitempty"`
	RunID                 string                             `json:"run_id,omitempty"`
	AcceptanceID          string                             `json:"acceptance_id,omitempty"`
	Reviewer              string                             `json:"reviewer,omitempty"`
	HeadSHA               string                             `json:"head_sha"`
	BaseSHA               string                             `json:"base_sha"`
	Requirements          string                             `json:"requirements_digest"`
	Result                *SharedReviewResult                `json:"result,omitempty"`
}

// SharedReviewBudgetAuthorization is a real external user's one-round grant.
// The authenticated actor remains in the original request record.
type SharedReviewBudgetAuthorization struct {
	RequestID string `json:"request_id"`
	Round     int    `json:"round"`
	Reason    string `json:"reason"`
}

type SharedDeliveryReview struct {
	BudgetAuthorizations       []SharedReviewBudgetAuthorization `json:"budget_authorizations,omitempty"`
	AssessedRequirementsDigest string                            `json:"assessed_requirements_digest,omitempty"`
	AssessmentReason           string                            `json:"assessment_reason,omitempty"`
	ReviewID                   string                            `json:"review_id"`
	Provider                   IssueDeliveryProvider             `json:"provider"`
	Repository                 string                            `json:"repository"`
	Number                     int64                             `json:"number"`
	Revision                   int64                             `json:"revision"`
	Status                     string                            `json:"status"`
	Reviewer                   string                            `json:"reviewer,omitempty"`
	HeadSHA                    string                            `json:"head_sha"`
	BaseSHA                    string                            `json:"base_sha"`
	RequirementsDigest         string                            `json:"requirements_digest"`
	Valid                      bool                              `json:"valid"`
	InvalidReason              string                            `json:"invalid_reason,omitempty"`
	Links                      []SharedDeliveryReviewLink        `json:"links"`
	Rounds                     []SharedDeliveryReviewRound       `json:"rounds"`
	Dispositions               []SharedReviewDisposition         `json:"dispositions,omitempty"`
	EscalationIssueID          string                            `json:"escalation_issue_id,omitempty"`
	Reason                     string                            `json:"reason,omitempty"`
}

type SharedDeliveryReviewPage struct {
	Items []SharedDeliveryReview `json:"items"`
	More  bool                   `json:"more"`
	Next  string                 `json:"next,omitempty"`
}

// UpdateSharedDeliveryReviewPayload coordinates a single review of an entire PR.
// record reads the bound review Task's actual artifact, never a caller's verdict.
type UpdateSharedDeliveryReviewPayload struct {
	IssueControlPayload
	ExpectedReviewRevision *int64                    `json:"expected_review_revision,omitempty"`
	DeliveryID             string                    `json:"delivery_id"`
	Action                 string                    `json:"action"`
	TaskID                 string                    `json:"task_id,omitempty"`
	ReviewerTaskID         string                    `json:"reviewer_task_id,omitempty"`
	AcceptanceID           string                    `json:"acceptance_id,omitempty"`
	Dispositions           []SharedReviewDisposition `json:"dispositions,omitempty"`
}
