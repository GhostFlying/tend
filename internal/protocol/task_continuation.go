package protocol

// ContinueTaskPayload resumes the same collaboration goal without revising its
// immutable agreement. Reason describes the next step for rebuilt context.
type ContinueTaskPayload struct {
	Reason                  string `json:"reason"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch,omitempty"`
}

// TaskExecutionContext is a bounded, reconstructible index. Artifact bodies and
// original checks remain available through read_context rather than copied here.
type TaskExecutionContext struct {
	Generation         int64                 `json:"generation"`
	NextStep           string                `json:"next_step,omitempty"`
	PreviousArtifactID string                `json:"previous_artifact_id,omitempty"`
	Runs               []RunView             `json:"runs"`
	Artifacts          []TaskResultReference `json:"artifacts"`
	Reviews            []TaskReviewTarget    `json:"reviews"`
	More               bool                  `json:"more"`
}

type TaskResultReference struct {
	ArtifactID  string `json:"artifact_id"`
	RunID       string `json:"run_id"`
	SpecVersion int64  `json:"spec_version"`
	Generation  int64  `json:"generation"`
	SHA256      string `json:"sha256"`
}

type TaskReviewTarget struct {
	ReviewID           string                `json:"review_id"`
	Provider           IssueDeliveryProvider `json:"provider"`
	Repository         string                `json:"repository"`
	Number             int64                 `json:"number"`
	Revision           int64                 `json:"revision"`
	Status             string                `json:"status"`
	HeadSHA            string                `json:"head_sha"`
	BaseSHA            string                `json:"base_sha"`
	RequirementsDigest string                `json:"requirements_digest"`
	Reviewer           string                `json:"reviewer"`
}
