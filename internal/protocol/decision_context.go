package protocol

// CapabilityWorkerQuestionContext negotiates the optional structured question extension.
const CapabilityWorkerQuestionContext = "worker-question-context-v1"

// DecisionIssue separates execution failures from missing evidence or authority.
type DecisionIssue struct {
	IssueID    string `json:"issue_id"`
	Category   string `json:"category"`
	Summary    string `json:"summary"`
	Impact     string `json:"impact"`
	NextAction string `json:"next_action"`
	Actor      string `json:"actor"`
	Status     string `json:"status"`
	Source     string `json:"source"`
}

// DecisionBriefing is a coordinator explanation, never an owner answer or a
// replacement of the original worker question. Revisions are retained separately.
type DecisionBriefing struct {
	Revision        int64           `json:"revision"`
	Summary         string          `json:"summary"`
	OwnerChoice     string          `json:"owner_choice"`
	Issues          []DecisionIssue `json:"issues"`
	SourceMessageID string          `json:"source_message_id"`
	CreatedAt       int64           `json:"created_at"`
}

// ExplainDecisionPayload binds an explanation to the original question version.
type ExplainDecisionPayload struct {
	DecisionVersion int64           `json:"decision_version"`
	Summary         string          `json:"summary"`
	OwnerChoice     string          `json:"owner_choice"`
	Issues          []DecisionIssue `json:"issues"`
	SourceMessageID string          `json:"source_message_id"`
}
