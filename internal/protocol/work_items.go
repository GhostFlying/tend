package protocol

// IssuePlan is the editable responsibility ledger. Existing stable IDs and
// task links survive revisions; implementation designs belong in task specs.
type IssuePlan struct {
	Title                  string          `json:"title"`
	Project                string          `json:"project"`
	Intent                 string          `json:"intent"`
	Background             []SharedFact    `json:"background,omitempty"`
	Stages                 []IssueStage    `json:"stages"`
	FollowOns              []IssueFollowOn `json:"follow_ons,omitempty"`
	Deliveries             []IssueDelivery `json:"deliveries,omitempty"`
	DefaultWorkspaceTaskID string          `json:"default_workspace_task_id,omitempty"`
}

// WorkItem is the current ledger revision, with immutable historical snapshots.
type WorkItem struct {
	IssuePlan
	WorkItemID string         `json:"work_item_id"`
	Revision   int64          `json:"revision"`
	Status     WorkItemStatus `json:"status"`
	Closure    *IssueClosure  `json:"closure,omitempty"`
	CreatedAt  int64          `json:"created_at"`
	UpdatedAt  int64          `json:"updated_at"`
}

// IssueEvidence cites an acceptance, external observation or owner decision.
type IssueEvidence struct {
	Kind      IssueEvidenceKind `json:"kind"`
	Reference string            `json:"reference"`
	Summary   string            `json:"summary"`
	// Observation is a server-frozen provider fact, never caller supplied proof.
	Observation *DeliveryObservation `json:"observation,omitempty"`
	Upgrade     *UpgradeEvidence     `json:"upgrade,omitempty"`
}

// IssueStage has caller-chosen stable identity within its work item.
type IssueStage struct {
	StageID   string           `json:"stage_id"`
	Title     string           `json:"title"`
	ParentID  string           `json:"parent_id,omitempty"`
	DependsOn []string         `json:"depends_on,omitempty"`
	Status    IssueStageStatus `json:"status"`
	Reason    string           `json:"reason,omitempty"`
	Evidence  []IssueEvidence  `json:"evidence,omitempty"`
	TaskIDs   []string         `json:"task_ids,omitempty"`
}

// IssueFollowOn preserves commitments even when their stage is retired.
type IssueFollowOn struct {
	FollowOnID    string              `json:"follow_on_id"`
	Kind          IssueFollowOnKind   `json:"kind"`
	Text          string              `json:"text"`
	Source        string              `json:"source"`
	Status        IssueFollowOnStatus `json:"status"`
	TransferredTo string              `json:"transferred_to,omitempty"`
	Evidence      []IssueEvidence     `json:"evidence,omitempty"`
}

// IssueDelivery is an immutable external target identity. Observation facts
// are stored independently so editing the plan cannot overwrite newer facts.
type IssueDelivery struct {
	DeliveryID string                `json:"delivery_id"`
	Provider   IssueDeliveryProvider `json:"provider"`
	Repository string                `json:"repository"`
	Number     int64                 `json:"number"`
	URL        string                `json:"url,omitempty"`
	HostID     string                `json:"host_id,omitempty"`
}

// IssueClosure records an explicit responsibility endpoint.
type IssueClosure struct {
	Kind     IssueClosureKind `json:"kind"`
	Reason   string           `json:"reason"`
	Evidence []IssueEvidence  `json:"evidence"`
}

// IssueTaskDisposition explicitly acknowledges the agreement of an active
// task when the surrounding plan changes; it never mutates that task's spec.
type IssueTaskDisposition struct {
	TaskID      string                     `json:"task_id"`
	Action      IssueTaskDispositionAction `json:"action"`
	SpecVersion int64                      `json:"spec_version"`
	Reason      string                     `json:"reason"`
}

// CreateWorkItemPayload supplies the initial plan; it always starts open.
type InitialWorkItemContext struct {
	SourceReviews    []LogicalTurnReview `json:"source_reviews,omitempty"`
	Coverage         string              `json:"coverage"`
	Gaps             string              `json:"gaps"`
	SourceMessageIDs []string            `json:"source_message_ids"`
	Facts            []ContextFact       `json:"facts"`
	ReviewedKinds    []string            `json:"reviewed_kinds"`
}

type CreateWorkItemPayload struct {
	InitialContext *InitialWorkItemContext `json:"initial_context,omitempty"`
	IssuePlan
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
}

// UpdateWorkItemPayload replaces the plan under expected_control_revision.
type UpdateWorkItemPayload struct {
	CompletionReviews []CompletionReviewAcknowledgement `json:"completion_reviews,omitempty"`
	SourceMessageID   string                            `json:"source_message_id,omitempty"`
	CommitSeq         int64                             `json:"commit_seq,omitempty"`
	IssuePlan
	Status           WorkItemUpdateStatus   `json:"status,omitempty"`
	TaskDispositions []IssueTaskDisposition `json:"task_dispositions,omitempty"`
}

// CloseWorkItemPayload supplies the explicit closure evidence.
type CloseWorkItemPayload struct {
	SourceMessageID string       `json:"source_message_id,omitempty"`
	CommitSeq       int64        `json:"commit_seq,omitempty"`
	Closure         IssueClosure `json:"closure"`
}

// ReopenWorkItemPayload records why responsibility resumed.
type ReopenWorkItemPayload struct {
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
	Reason          string `json:"reason"`
}

// LinkWorkItemTaskPayload links an existing task to one or more stages.
type LinkWorkItemTaskPayload struct {
	SourceMessageID string   `json:"source_message_id,omitempty"`
	CommitSeq       int64    `json:"commit_seq,omitempty"`
	TaskID          string   `json:"task_id"`
	StageIDs        []string `json:"stage_ids"`
}

// CompletionReviewAcknowledgement records a planner's assessment, without
// deriving stage completion or a responsibility endpoint from task completion.
type CompletionReviewAcknowledgement struct {
	EventID string `json:"event_id"`
	Reason  string `json:"reason"`
}

// WorkItemCompletionReview is durable work left by a task completion event.
type WorkItemCompletionReview struct {
	EventID      string `json:"event_id"`
	TaskID       string `json:"task_id"`
	AcceptanceID string `json:"acceptance_id"`
	CreatedAt    int64  `json:"created_at"`
}
