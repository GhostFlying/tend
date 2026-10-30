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

type IssueBoardCard struct {
	Status            IssueWorkStatus `json:"status"`
	StageTitle        string          `json:"stage_title,omitempty"`
	StagesTotal       int             `json:"stages_total"`
	StagesDone        int             `json:"stages_done"`
	Execution         ExecutionCounts `json:"execution"`
	AttentionCount    int             `json:"attention_count"`
	NotificationDueAt int64           `json:"notification_due_at,omitempty"`
}

type SetIssueStatusPayload struct {
	ExpectedAssignmentEpoch int64           `json:"expected_assignment_epoch"`
	Status                  IssueWorkStatus `json:"status"`
}

type IssueNotificationSettings struct {
	Revision     int64 `json:"revision"`
	DelayMinutes int   `json:"delay_minutes"`
}

type SetIssueNotificationSettingsPayload struct {
	DelayMinutes int `json:"delay_minutes"`
}
