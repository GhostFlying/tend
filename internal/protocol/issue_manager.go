package protocol

import "encoding/json"

// TaskKind distinguishes delivery work from continuing coordination responsibility.
type TaskKind string

const (
	TaskKindExecution           TaskKind = "execution"
	TaskKindIssueManager        TaskKind = "issue_manager"
	DefaultIssueManagerCategory          = "issue-manager"
	CapabilityIssueManager               = "runner-issue-manager-v1"
	OpIssueRequestManager                = "issues.request_manager"
)

// RequestIssueManagerPayload selects a category, never a particular machine.
type RequestIssueManagerPayload struct {
	Replace                 bool   `json:"replace,omitempty"`
	CategoryID              string `json:"category_id,omitempty"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	SourceMessageID         string `json:"source_message_id,omitempty"`
	Reason                  string `json:"reason"`
}

// IssueManagerSpec is an immutable responsibility contract, not a code workspace.
type IssueManagerSpec struct {
	SourceContentRevision     int64  `json:"source_content_revision,omitempty"`
	SourceAssociationRevision int64  `json:"source_association_revision,omitempty"`
	ReceiptOnly               bool   `json:"receipt_only,omitempty"`
	IssueID                   string `json:"issue_id"`
	CategoryID                string `json:"category_id"`
	ExpectedAssignmentEpoch   int64  `json:"expected_assignment_epoch"`
	SourceMessageID           string `json:"source_message_id,omitempty"`
	Reason                    string `json:"reason"`
}

// IssueManagerAssignment binds a turn to a continuing responsibility.
type IssueManagerAssignment struct {
	ReceiptOnly     bool   `json:"receipt_only,omitempty"`
	IssueID         string `json:"issue_id"`
	ConversationID  string `json:"conversation_id"`
	AssignmentEpoch int64  `json:"assignment_epoch"`
	CategoryID      string `json:"category_id"`
}

// ManagerNativeRequest is a durable native operation routed to its leased executor.
// Payload contains no credentials or server filesystem locators.
type ManagerNativeRequest struct {
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

const (
	OpManagerNativeClaim   = "manager.native_claim"
	OpManagerNativeReply   = "manager.native_reply"
	OpManagerNativeEvent   = "manager.native_event"
	OpManagerNativeStopped = "manager.native_stopped"
)

type ManagerNativeReply struct {
	LastSequence int64           `json:"last_sequence,omitempty"`
	ID           string          `json:"id"`
	Result       json.RawMessage `json:"result"`
	Error        string          `json:"error,omitempty"`
	Rejected     bool            `json:"rejected,omitempty"`
}

type ManagerNativeEvent struct {
	Sequence int64           `json:"sequence"`
	Body     json.RawMessage `json:"body"`
}

// IssueManagerView separates published category work from its actual assignee.
type IssueManagerView struct {
	ControlMode           ControlMode        `json:"control_mode,omitempty"`
	ReconciliationPending bool               `json:"reconciliation_pending,omitempty"`
	Recovery              *AgentRecoveryView `json:"recovery,omitempty"`
	ConversationID        string             `json:"conversation_id,omitempty"`
	NativeThreadID        string             `json:"native_thread_id,omitempty"`
	RunID                 string             `json:"run_id,omitempty"`
	TaskID                string             `json:"task_id"`
	CategoryID            string             `json:"category_id"`
	State                 TaskLifecycle      `json:"state"`
	HostID                string             `json:"host_id,omitempty"`
	Agent                 *AgentIdentity     `json:"agent,omitempty"`
	Execution             *RunExecutionPin   `json:"execution,omitempty"`
	QueueReason           *QueueWaitReason   `json:"queue_reason,omitempty"`
}

// ManagerStoppedPayload confirms process exit only after all earlier events.
// It does not turn an uncertain business operation into a successful result.
type ManagerStoppedPayload struct {
	Reason       string `json:"reason"`
	LastSequence int64  `json:"last_sequence"`
}

// ManagerMigrationPayload is an owner-controlled, resumable per-Issue switch.
// switch creates a consistent backup before fencing the old executor.
type ManagerMigrationPayload struct {
	Phase                   string `json:"phase"` // preflight, switch, status
	CategoryID              string `json:"category_id,omitempty"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	Reason                  string `json:"reason,omitempty"`
}
