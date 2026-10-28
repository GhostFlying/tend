package protocol

// IssueState is the canonical responsibility lifecycle, independent of legacy status.
type IssueState string

const (
	IssueActive     IssueState = "active"
	IssuePaused     IssueState = "paused"
	IssueCancelling IssueState = "cancelling"
	IssueCancelled  IssueState = "cancelled"
	IssueClosed     IssueState = "closed"
)

type IssueAssigneeKind string

const (
	IssueAssigneeOwner IssueAssigneeKind = "owner"
	IssueAssigneeAgent IssueAssigneeKind = "agent"
)

// IssueAssignee is a concrete identity reference; validation requires a trusted resolver.
type IssueAssignee struct {
	Kind IssueAssigneeKind `json:"kind"`
	Ref  string            `json:"ref"`
}

// IssueControl has no independent revision: work_items.revision owns the aggregate.
type IssueControl struct {
	IssueID          string         `json:"issue_id"`
	ParentIssueID    string         `json:"parent_issue_id,omitempty"`
	State            IssueState     `json:"state"`
	Assignee         IssueAssignee  `json:"assignee"`
	AssignmentEpoch  int64          `json:"assignment_epoch"`
	AssignmentState  string         `json:"assignment_state"`
	PendingAssignee  *IssueAssignee `json:"pending_assignee,omitempty"`
	PendingHandoffID string         `json:"pending_handoff_id,omitempty"`
}

type IssueDependency struct {
	IssueID   string `json:"issue_id"`
	DependsOn string `json:"depends_on"`
}

// IssueControlSnapshot shares the immutable plan revision and records all edges.
type IssueControlSnapshot struct {
	Revision     int64             `json:"revision"`
	Control      IssueControl      `json:"control"`
	Dependencies []IssueDependency `json:"dependencies"`
}

// IssueBlocker is a precise fact, not a derived UI label or authority grant.
type IssueBlocker struct {
	Code      string `json:"code"`
	ObjectID  string `json:"object_id"`
	Revision  int64  `json:"revision,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// Issue is the canonical aggregate shared by owner API and coordination.
// Legacy ledger identities remain opaque; the public identity is issue_id.
type Issue struct {
	IssueControl
	Revision  int64            `json:"revision"`
	Plan      WorkItemPlan     `json:"plan"`
	DependsOn []string         `json:"depends_on"`
	Closure   *WorkItemClosure `json:"closure,omitempty"`
	CreatedAt int64            `json:"created_at"`
	UpdatedAt int64            `json:"updated_at"`
}
