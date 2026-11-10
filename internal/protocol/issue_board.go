package protocol

// IssueWorkStatus expresses work intent, independently of execution facts.
// Done and cancelled are read projections of the canonical lifecycle only.
type IssueWorkStatus string

const (
	IssueBacklog       IssueWorkStatus = "backlog"
	IssueTodo          IssueWorkStatus = "todo"
	IssueInProgress    IssueWorkStatus = "in_progress"
	IssueInReview      IssueWorkStatus = "in_review"
	IssueDone          IssueWorkStatus = "done"
	IssueWorkCancelled IssueWorkStatus = "cancelled"
)

func IssueWorkStatusValues() []IssueWorkStatus {
	return []IssueWorkStatus{IssueBacklog, IssueTodo, IssueInProgress, IssueInReview, IssueDone, IssueWorkCancelled}
}

// BoardStatus never turns an execution result into a responsibility endpoint.
func (c IssueControl) BoardStatus() IssueWorkStatus {
	switch c.State {
	case IssueClosed:
		return IssueDone
	case IssueCancelled:
		return IssueWorkCancelled
	}
	if c.WorkStatus == "" {
		return IssueTodo
	}
	return c.WorkStatus
}

type IssuePauseProgress struct {
	Pending       int64  `json:"pending"`
	Stopped       int64  `json:"stopped"`
	Unsupported   int64  `json:"unsupported"`
	IssueID       string `json:"issue_id"`
	IssueRevision int64  `json:"issue_revision"`
}

type IssueBoardCard struct {
	RetainedExecution        bool                `json:"retained_execution,omitempty"`
	QueueReason              *QueueWaitReason    `json:"queue_reason,omitempty"`
	Pause                    *IssuePauseProgress `json:"pause,omitempty"`
	Progress                 *IssueProgress      `json:"progress,omitempty"`
	Children                 *IssueChildProgress `json:"children,omitempty"`
	PendingManagerCategoryID string              `json:"pending_manager_category_id,omitempty"`
	Status                   IssueWorkStatus     `json:"status"`
	StageTitle               string              `json:"stage_title,omitempty"`
	StagesTotal              int                 `json:"stages_total"`
	StagesDone               int                 `json:"stages_done"`
	Execution                ExecutionCounts     `json:"execution"`
	AttentionCount           int                 `json:"attention_count"`
	OwnerAttentionCount      int                 `json:"owner_attention_count,omitempty"`
	NotificationDueAt        int64               `json:"notification_due_at,omitempty"`
}

type SetIssueStatusPayload struct {
	Owner                   *IssueOwnerRequest `json:"owner,omitempty"`
	ExpectedAssignmentEpoch int64              `json:"expected_assignment_epoch"`
	Status                  IssueWorkStatus    `json:"status"`
}

// IssueChildProgress summarizes direct responsibilities, not execution sessions.
// Items is an activity-prioritized preview; the parent-filtered Issue listing
// provides the complete, paginated set.
type IssueChildProgress struct {
	Total     int                 `json:"total"`
	Done      int                 `json:"done"`
	Cancelled int                 `json:"cancelled"`
	Running   int                 `json:"running"`
	Items     []IssueChildSummary `json:"items"`
}

type IssueChildSummary struct {
	IssueID   string          `json:"issue_id"`
	Title     string          `json:"title"`
	Status    IssueWorkStatus `json:"status"`
	Execution ExecutionCounts `json:"execution"`
}

type IssueNotificationSettings struct {
	Revision     int64 `json:"revision"`
	DelayMinutes int   `json:"delay_minutes"`
}

type SetIssueNotificationSettingsPayload struct {
	DelayMinutes int `json:"delay_minutes"`
}
