package protocol

// WorkItemPlan is the editable responsibility ledger. Existing stable IDs and
// task links survive revisions; implementation designs belong in task specs.
type WorkItemPlan struct {
	Title                  string             `json:"title"`
	Project                string             `json:"project"`
	Intent                 string             `json:"intent"`
	Background             []SharedFact       `json:"background,omitempty"`
	Stages                 []WorkItemStage    `json:"stages"`
	FollowOns              []WorkItemFollowOn `json:"follow_ons,omitempty"`
	Deliveries             []WorkItemDelivery `json:"deliveries,omitempty"`
	DefaultWorkspaceTaskID string             `json:"default_workspace_task_id,omitempty"`
}

// WorkItem is the current ledger revision, with immutable historical snapshots.
type WorkItem struct {
	WorkItemPlan
	WorkItemID string           `json:"work_item_id"`
	Revision   int64            `json:"revision"`
	Status     WorkItemStatus   `json:"status"`
	Closure    *WorkItemClosure `json:"closure,omitempty"`
	CreatedAt  int64            `json:"created_at"`
	UpdatedAt  int64            `json:"updated_at"`
}

// WorkItemEvidence cites an acceptance, external observation or owner decision.
type WorkItemEvidence struct {
	Kind      WorkItemEvidenceKind `json:"kind"`
	Reference string               `json:"reference"`
	Summary   string               `json:"summary"`
	// Observation is a server-frozen provider fact, never caller supplied proof.
	Observation *DeliveryObservation `json:"observation,omitempty"`
	Upgrade     *UpgradeEvidence     `json:"upgrade,omitempty"`
}

// WorkItemStage has caller-chosen stable identity within its work item.
type WorkItemStage struct {
	StageID   string              `json:"stage_id"`
	Title     string              `json:"title"`
	ParentID  string              `json:"parent_id,omitempty"`
	DependsOn []string            `json:"depends_on,omitempty"`
	Status    WorkItemStageStatus `json:"status"`
	Reason    string              `json:"reason,omitempty"`
	Evidence  []WorkItemEvidence  `json:"evidence,omitempty"`
	TaskIDs   []string            `json:"task_ids,omitempty"`
}

// WorkItemFollowOn preserves commitments even when their stage is retired.
type WorkItemFollowOn struct {
	FollowOnID    string                 `json:"follow_on_id"`
	Kind          WorkItemFollowOnKind   `json:"kind"`
	Text          string                 `json:"text"`
	Source        string                 `json:"source"`
	Status        WorkItemFollowOnStatus `json:"status"`
	TransferredTo string                 `json:"transferred_to,omitempty"`
	Evidence      []WorkItemEvidence     `json:"evidence,omitempty"`
}

// WorkItemDelivery is an immutable external target identity. Observation facts
// are stored independently so editing the plan cannot overwrite newer facts.
type WorkItemDelivery struct {
	DeliveryID string                   `json:"delivery_id"`
	Provider   WorkItemDeliveryProvider `json:"provider"`
	Repository string                   `json:"repository"`
	Number     int64                    `json:"number"`
	URL        string                   `json:"url,omitempty"`
	HostID     string                   `json:"host_id,omitempty"`
}

// WorkItemClosure records an explicit responsibility endpoint.
type WorkItemClosure struct {
	Kind     WorkItemClosureKind `json:"kind"`
	Reason   string              `json:"reason"`
	Evidence []WorkItemEvidence  `json:"evidence"`
}

// WorkItemTaskDisposition explicitly acknowledges the agreement of an active
// task when the surrounding plan changes; it never mutates that task's spec.
type WorkItemTaskDisposition struct {
	TaskID      string                        `json:"task_id"`
	Action      WorkItemTaskDispositionAction `json:"action"`
	SpecVersion int64                         `json:"spec_version"`
	Reason      string                        `json:"reason"`
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
	WorkItemPlan
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
}

// UpdateWorkItemPayload replaces the plan under expected_control_revision.
type UpdateWorkItemPayload struct {
	CompletionReviews []CompletionReviewAcknowledgement `json:"completion_reviews,omitempty"`
	SourceMessageID   string                            `json:"source_message_id,omitempty"`
	CommitSeq         int64                             `json:"commit_seq,omitempty"`
	WorkItemPlan
	Status           WorkItemUpdateStatus      `json:"status,omitempty"`
	TaskDispositions []WorkItemTaskDisposition `json:"task_dispositions,omitempty"`
}

// CloseWorkItemPayload supplies the explicit closure evidence.
type CloseWorkItemPayload struct {
	SourceMessageID string          `json:"source_message_id,omitempty"`
	CommitSeq       int64           `json:"commit_seq,omitempty"`
	Closure         WorkItemClosure `json:"closure"`
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
