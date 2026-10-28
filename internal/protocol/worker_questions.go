package protocol

import "encoding/json"

const (
	CapabilityWorkerQuestions = "worker-questions-v1"
	OpRunsAskOwner            = "runs.ask_owner"
)

// AskOwnerPayload identifies a question durably within its current run.
type AskOwnerPayload struct {
	Issues      []DecisionIssue `json:"issues,omitempty"`
	QuestionKey string          `json:"question_key"`
	Question    string          `json:"question"`
	Evidence    string          `json:"evidence,omitempty"`
	Options     []string        `json:"options,omitempty"`
}

// WorkerQuestionSource identifies the worker behind an owner decision.
type WorkerQuestionSource struct {
	Issues            []DecisionIssue `json:"issues,omitempty"`
	RunID             string          `json:"run_id"`
	Role              RunRole         `json:"role"`
	SpecVersion       int64           `json:"spec_version"`
	QuestionKey       string          `json:"question_key"`
	Evidence          string          `json:"evidence,omitempty"`
	QuestionMessageID string          `json:"question_message_id"`
}

// WorkerQuestion is replayable: reading an answer never consumes it.
type WorkerQuestion struct {
	Issues            []DecisionIssue `json:"issues,omitempty"`
	DecisionID        string          `json:"decision_id"`
	RunID             string          `json:"run_id"`
	ActionID          string          `json:"action_id"`
	SpecVersion       int64           `json:"spec_version"`
	Role              RunRole         `json:"role"`
	QuestionKey       string          `json:"question_key"`
	Question          string          `json:"question"`
	Evidence          string          `json:"evidence,omitempty"`
	Options           []string        `json:"options,omitempty"`
	Version           int64           `json:"version"`
	Status            string          `json:"status"`
	Answer            json.RawMessage `json:"answer,omitempty"`
	QuestionMessageID string          `json:"question_message_id"`
}
