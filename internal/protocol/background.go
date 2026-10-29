package protocol

import "encoding/json"

type BackgroundBatch struct {
	BackgroundBatchState
	WorkItemID string `json:"work_item_id,omitempty"`
}

// BackgroundBatchState is shared owner metadata; responsibility identity is projected by its caller.
type BackgroundBatchState struct {
	BatchID     string             `json:"batch_id"`
	MessageID   string             `json:"message_id"`
	Status      string             `json:"status"`
	SourceCount int                `json:"source_count"`
	CreatedAt   int64              `json:"created_at"`
	FrozenAt    int64              `json:"frozen_at,omitempty"`
	Checkin     *BackgroundCheckin `json:"checkin,omitempty"`
}
type BackgroundSource struct {
	Seq          int64           `json:"seq"`
	MessageID    string          `json:"message_id"`
	EventID      string          `json:"event_id"`
	Kind         string          `json:"kind"`
	RuleID       string          `json:"rule_id,omitempty"`
	OccurrenceID string          `json:"occurrence_id,omitempty"`
	CancelledAt  int64           `json:"cancelled_at,omitempty"`
	Body         string          `json:"body"`
	Data         json.RawMessage `json:"data,omitempty"`
}
type BackgroundSourcePage struct {
	Batch   BackgroundBatch    `json:"batch"`
	Sources []BackgroundSource `json:"sources"`
	More    bool               `json:"more"`
	Next    int64              `json:"next,omitempty"`
}
type BackgroundCheckin struct {
	ThreadID    string   `json:"thread_id,omitempty"`
	TurnID      string   `json:"turn_id,omitempty"`
	Note        string   `json:"note"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	CreatedAt   int64    `json:"created_at"`
}
type CoordinationMetrics struct {
	OwnerDepth          int   `json:"owner_depth"`
	BackgroundDepth     int   `json:"background_depth"`
	OldestOwnerAt       int64 `json:"oldest_owner_at,omitempty"`
	OldestBackgroundAt  int64 `json:"oldest_background_at,omitempty"`
	MaxDispatchDelayMS  int64 `json:"max_dispatch_delay_ms"`
	WithdrawalConflicts int   `json:"withdrawal_conflicts"`
	CoalescedSources    int   `json:"coalesced_sources"`
	SilentChecks        int   `json:"silent_checks"`
	UnknownBatches      int   `json:"unknown_batches"`
}
