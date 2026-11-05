package protocol

// DecisionContinuationMode separates an answer from permission to schedule.
type DecisionContinuationMode string

const (
	DecisionContinueNow DecisionContinuationMode = "continue_now"
	DecisionWait        DecisionContinuationMode = "wait"
	DecisionDependency  DecisionContinuationMode = "dependency"

	OpAnswerDecisionContinuation    = "decisions.respond_continuation"
	OpResolveDecisionContinuation   = "decisions.resolve_continuation_from_message"
	OpResumeDecisionContinuation    = "decisions.continuation.resume"
	OpReconcileDecisionContinuation = "decisions.continuation.reconcile"
)

// DecisionContinuationDependency accepts completion of one frozen ordinary execution Task spec.
// Coordination/manager tasks are not supported dependency targets.
type DecisionContinuationDependency struct {
	TaskID      string `json:"task_id"`
	SpecVersion int64  `json:"spec_version"`
}

// DecisionContinuationPolicy is recorded with the immutable answer. Free text
// never supplies readiness or changes this policy after the answer is recorded.
type DecisionContinuationPolicy struct {
	Mode       DecisionContinuationMode        `json:"mode"`
	Reason     string                          `json:"reason,omitempty"`
	Dependency *DecisionContinuationDependency `json:"dependency,omitempty"`
}

// ResumeDecisionContinuationPayload addresses the independently versioned
// continuation, in addition to the task control revision in the envelope.
type ResumeDecisionContinuationPayload struct {
	ExpectedRevision int64  `json:"expected_revision"`
	SourceMessageID  string `json:"source_message_id,omitempty"`
}

// DecisionContinuationView distinguishes recorded intent, physical readiness,
// and an already committed successor. Legacy has no recorded policy; a ready
// typed intent can still have a blocker.
type DecisionContinuationView struct {
	DecisionID           string                      `json:"decision_id"`
	RunID                string                      `json:"run_id"`
	TaskID               string                      `json:"task_id"`
	Revision             int64                       `json:"revision"`
	DecisionVersion      int64                       `json:"decision_version"`
	TaskControlRevision  int64                       `json:"task_control_revision"`
	Policy               *DecisionContinuationPolicy `json:"policy,omitempty"`
	State                string                      `json:"state"`
	ResumeCondition      string                      `json:"resume_condition"`
	BlockedReason        string                      `json:"blocked_reason,omitempty"`
	ContinuationActionID string                      `json:"continuation_action_id,omitempty"`
	SuccessorRunID       string                      `json:"successor_run_id,omitempty"`
	AnswerRequestID      string                      `json:"answer_request_id,omitempty"`
	ResumeRequestID      string                      `json:"resume_request_id,omitempty"`
}
