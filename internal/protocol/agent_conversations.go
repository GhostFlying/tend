package protocol

// AgentConversation is a logical conversation; native thread generations are separate.
type AgentConversation struct {
	ID             string `json:"conversation_id"`
	WorkItemID     string `json:"work_item_id,omitempty"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Revision       int64  `json:"revision"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	LastMessageSeq int64  `json:"last_message_seq"`
	LastMessageAt  int64  `json:"last_message_at"`
	Preview        string `json:"preview"`
}
type AgentConversationPage struct {
	RuntimeAvailable bool                `json:"runtime_available"`
	Items            []AgentConversation `json:"items"`
	Next             string              `json:"next,omitempty"`
}
type MessageWorkItem struct {
	WorkItemID  string `json:"work_item_id"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	OwnerLocked bool   `json:"owner_locked"`
}
type MessageProcessingState struct {
	LogicalTurnID string `json:"logical_turn_id"`
	Status        string `json:"status"`
	Revision      int64  `json:"revision"`
	Reason        string `json:"reason"`
}

type MessageAssociations struct {
	Processing      *MessageProcessingState `json:"processing,omitempty"`
	Disposition     string                  `json:"disposition"`
	ContentRevision int64                   `json:"content_revision"`
	FactsReviewed   bool                    `json:"facts_reviewed"`
	OwnerLocked     bool                    `json:"owner_locked"`
	MessageID       string                  `json:"message_id"`
	Revision        int64                   `json:"revision"`
	Items           []MessageWorkItem       `json:"items"`
}
type SetMessageAssociationsPayload struct {
	Disposition     string   `json:"disposition"`
	ContentRevision int64    `json:"content_revision"`
	WorkItemIDs     []string `json:"work_item_ids"`
}
type ContextFact struct {
	ID              string `json:"fact_id"`
	WorkItemID      string `json:"work_item_id,omitempty"`
	Scope           string `json:"scope"`
	Project         string `json:"project,omitempty"`
	Kind            string `json:"kind"`
	Body            string `json:"body"`
	SourceMessageID string `json:"source_message_id"`
	Attribution     string `json:"attribution"`
	Supersedes      string `json:"supersedes,omitempty"`
	NeedsReview     bool   `json:"needs_review"`
}
type WorkItemContext struct {
	DispatchReady     bool               `json:"dispatch_ready"`
	CoordinationState string             `json:"coordination_state"`
	DispatchBlockers  []ContextBlocker   `json:"dispatch_blockers"`
	SyncStatus        string             `json:"sync_status"`
	Watermark         int64              `json:"watermark"`
	WorkItemID        string             `json:"work_item_id"`
	Revision          int64              `json:"revision"`
	Prepared          bool               `json:"prepared"`
	Coverage          string             `json:"coverage"`
	Gaps              string             `json:"gaps"`
	Facts             []ContextFact      `json:"facts"`
	Sources           []ConversationItem `json:"sources"`
	More              bool               `json:"more"`
	Next              int64              `json:"next,omitempty"`
}
type PrepareWorkItemContextPayload struct {
	RetiredFactIDs   []string `json:"retired_fact_ids,omitempty"`
	ConfirmedFactIDs []string `json:"confirmed_fact_ids,omitempty"`
	Coverage         string   `json:"coverage"`
	Gaps             string   `json:"gaps"`
}
type BackgroundAssessmentPayload struct {
	ContextRevision int64    `json:"context_revision"`
	Disposition     string   `json:"disposition"`
	Note            string   `json:"note"`
	EvidenceIDs     []string `json:"evidence_ids"`
}

// AgentContextUsage keeps a coherent native snapshot, never cumulative billing usage.
type AgentContextUsage struct {
	Available        bool   `json:"available"`
	CompactSupported bool   `json:"compact_supported"`
	ThreadID         string `json:"thread_id,omitempty"`
	Revision         int64  `json:"revision"`
	UsedPercentage   *int64 `json:"used_percentage,omitempty"`
	ActiveTokens     *int64 `json:"active_tokens,omitempty"`
	EffectiveWindow  *int64 `json:"effective_window,omitempty"`
	Measurement      string `json:"measurement"`
	SampledAt        int64  `json:"sampled_at"`
	UpdatedAt        int64  `json:"updated_at"`
}
type AgentCompaction struct {
	ID             string             `json:"operation_id"`
	ConversationID string             `json:"conversation_id"`
	ThreadID       string             `json:"thread_id"`
	Status         string             `json:"status"`
	Source         string             `json:"source"`
	CreatedAt      int64              `json:"created_at"`
	UpdatedAt      int64              `json:"updated_at"`
	Before         *AgentContextUsage `json:"before,omitempty"`
	After          *AgentContextUsage `json:"after,omitempty"`
}
type AgentCompactionPage struct {
	Items []AgentCompaction `json:"items"`
	Next  int64             `json:"next,omitempty"`
}
type ConversationReturnLink struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
}

// AgentRolloutPage exposes collected native records and the thread lineage.
// Missing historical collection is explicit; projections do not pretend to be raw history.
type AgentRolloutPage struct {
	ConversationID string               `json:"conversation_id"`
	Coverage       string               `json:"coverage"`
	Threads        []string             `json:"threads"`
	Records        []AgentRolloutRecord `json:"records"`
	Next           int64                `json:"next"`
	More           bool                 `json:"more"`
}
type AgentRolloutRecord struct {
	Seq        int64  `json:"seq"`
	ThreadID   string `json:"thread_id"`
	TurnID     string `json:"turn_id"`
	ItemID     string `json:"item_id"`
	Kind       string `json:"kind"`
	Data       string `json:"data"`
	Characters int64  `json:"characters"`
	Truncated  bool   `json:"truncated"`
	CreatedAt  int64  `json:"created_at"`
}

// AgentRolloutWindow uses Unicode character offsets so JSON remains readable.
type AgentRolloutWindow struct {
	Text string `json:"text"`
	Next int64  `json:"next"`
	More bool   `json:"more"`
}

// LogicalTurnReview identifies the exact persisted versions reviewed by the agent.
type LogicalTurnReview struct {
	MessageID           string `json:"message_id"`
	ContentRevision     int64  `json:"content_revision"`
	AssociationRevision int64  `json:"association_revision"`
}
type LogicalTurnCommit struct {
	Messages []LogicalTurnReview    `json:"messages"`
	Contexts []LogicalContextReview `json:"contexts"`
}
type LogicalContextReview struct {
	WorkItemID string `json:"work_item_id"`
	Revision   int64  `json:"revision"`
}
type LogicalTurnState struct {
	CoordinationExecutionID string                 `json:"coordination_execution_id,omitempty"`
	NextEffectSeq           int64                  `json:"next_effect_seq,omitempty"`
	Awaiting                []PendingClarification `json:"awaiting"`
	ID                      string                 `json:"logical_turn_id"`
	Status                  string                 `json:"status"`
	Reason                  string                 `json:"reason"`
	RepairAttempts          int                    `json:"repair_attempts"`
	Messages                []MessageAssociations  `json:"messages"`
	Contexts                []LogicalContextReview `json:"contexts"`
}

type TurnClarification struct {
	Question string `json:"question"`
}

// PendingClarification identifies an unfinished responsibility without copying history.
type PendingClarification struct {
	LogicalTurnID string `json:"logical_turn_id"`
	MessageID     string `json:"message_id"`
	Revision      int64  `json:"revision"`
	Question      string `json:"question"`
}
type AdoptClarificationPayload struct {
	LogicalTurnID string `json:"logical_turn_id"`
	Revision      int64  `json:"expected_control_revision"`
}

// ContextBlocker identifies a semantic or lifecycle fence at an exact object.
type ContextBlocker struct {
	ControlRevision int64  `json:"control_revision,omitempty"`
	SpecVersion     int64  `json:"spec_version,omitempty"`
	Code            string `json:"code"`
	ObjectID        string `json:"object_id"`
	Revision        int64  `json:"revision"`
	Message         string `json:"message"`
}
