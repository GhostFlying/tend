package protocol

// IssueContextFact maps the existing persisted context to canonical terminology.
type IssueContextFact struct {
	ID              string `json:"fact_id"`
	IssueID         string `json:"issue_id,omitempty"`
	Scope           string `json:"scope"`
	Project         string `json:"project,omitempty"`
	Kind            string `json:"kind"`
	Body            string `json:"body"`
	SourceMessageID string `json:"source_message_id"`
	Attribution     string `json:"attribution"`
	Supersedes      string `json:"supersedes,omitempty"`
	NeedsReview     bool   `json:"needs_review"`
}

type IssueContextView struct {
	IssueID           string             `json:"issue_id"`
	Revision          int64              `json:"revision"`
	Prepared          bool               `json:"prepared"`
	Coverage          string             `json:"coverage"`
	Gaps              string             `json:"gaps"`
	DispatchReady     bool               `json:"dispatch_ready"`
	CoordinationState string             `json:"coordination_state"`
	DispatchBlockers  []ContextBlocker   `json:"dispatch_blockers"`
	SyncStatus        string             `json:"sync_status"`
	Watermark         int64              `json:"watermark"`
	Facts             []IssueContextFact `json:"facts"`
	Sources           []ConversationItem `json:"sources"`
	More              bool               `json:"more"`
	Next              int64              `json:"next,omitempty"`
}

type IssueFollowupPayload struct {
	ExpectedAssignmentEpoch int64         `json:"expected_assignment_epoch"`
	Spec                    *FollowupSpec `json:"spec,omitempty"`
}

type IssueFollowupRule struct {
	FollowupSpec
	IssueID      string `json:"issue_id"`
	RuleID       string `json:"rule_id"`
	DeadlineID   string `json:"deadline_id,omitempty"`
	Revision     int64  `json:"revision"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	NextAt       int64  `json:"next_at,omitempty"`
	TriggerCount int    `json:"trigger_count"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type IssueFollowupOccurrence struct {
	IssueID             string             `json:"issue_id"`
	BackgroundMessageID string             `json:"background_message_id,omitempty"`
	Checkin             *BackgroundCheckin `json:"checkin,omitempty"`
	OccurrenceID        string             `json:"occurrence_id"`
	RuleID              string             `json:"rule_id"`
	Revision            int64              `json:"revision"`
	MessageID           string             `json:"message_id"`
	Reason              string             `json:"reason"`
	Status              string             `json:"status"`
	Evidence            string             `json:"evidence"`
	CreatedAt           int64              `json:"created_at"`
}

type IssueFollowupPage struct {
	IssueID  string                             `json:"issue_id"`
	Items    []IssueFollowupRule                `json:"items"`
	Latest   map[string]IssueFollowupOccurrence `json:"latest,omitempty"`
	Enabled  bool                               `json:"enabled"`
	More     bool                               `json:"more"`
	Next     string                             `json:"next,omitempty"`
	EventSeq int64                              `json:"event_seq"`
}

type IssueFollowupHistory struct {
	Rule           IssueFollowupRule         `json:"rule"`
	Revisions      []IssueFollowupRule       `json:"revisions"`
	Occurrences    []IssueFollowupOccurrence `json:"occurrences"`
	More           bool                      `json:"more"`
	NextRevision   int64                     `json:"next_revision,omitempty"`
	NextOccurrence string                    `json:"next_occurrence,omitempty"`
}

type IssueDeliveryObservation struct {
	DeliverySnapshot
	IssueID        string `json:"issue_id"`
	DeliveryID     string `json:"delivery_id"`
	Generation     int64  `json:"generation"`
	CheckedAt      int64  `json:"checked_at,omitempty"`
	AttemptedAt    int64  `json:"attempted_at,omitempty"`
	ErrorKind      string `json:"error_kind,omitempty"`
	RefreshPending bool   `json:"refresh_pending,omitempty"`
}

type IssueDeliveryView struct {
	Delivery    IssueDelivery            `json:"delivery"`
	Observation IssueDeliveryObservation `json:"observation"`
}
type IssueDeliveryPage struct {
	IssueID string              `json:"issue_id"`
	Items   []IssueDeliveryView `json:"items"`
}
type IssueRefreshDeliveryPayload struct {
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	DeliveryID              string `json:"delivery_id"`
}

// CanonicalFollowupRule preserves the original rule identity and receipt data.
func CanonicalFollowupRule(r FollowupRule, deadline string) IssueFollowupRule {
	return IssueFollowupRule{FollowupSpec: r.FollowupSpec, IssueID: r.WorkItemID, RuleID: r.RuleID, DeadlineID: deadline, Revision: r.Revision, Status: r.Status, Reason: r.Reason, NextAt: r.NextAt, TriggerCount: r.TriggerCount, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func CanonicalFollowupOccurrence(o FollowupOccurrence) IssueFollowupOccurrence {
	return IssueFollowupOccurrence{IssueID: o.WorkItemID, BackgroundMessageID: o.BackgroundMessageID, Checkin: o.Checkin, OccurrenceID: o.OccurrenceID, RuleID: o.RuleID, Revision: o.Revision, MessageID: o.MessageID, Reason: o.Reason, Status: o.Status, Evidence: o.Evidence, CreatedAt: o.CreatedAt}
}
func CanonicalDeliveryObservation(o DeliveryObservation) IssueDeliveryObservation {
	return IssueDeliveryObservation{DeliverySnapshot: o.DeliverySnapshot, IssueID: o.WorkItemID, DeliveryID: o.DeliveryID, Generation: o.Generation, CheckedAt: o.CheckedAt, AttemptedAt: o.AttemptedAt, ErrorKind: o.ErrorKind, RefreshPending: o.RefreshPending}
}
