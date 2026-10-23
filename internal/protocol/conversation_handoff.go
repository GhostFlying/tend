package protocol

// ConversationHandoffView exposes shared summary policy and current coverage.
type ConversationHandoffView struct {
	Enabled            bool                         `json:"enabled"`
	IdleMinutes        int64                        `json:"idle_minutes"`
	Revision           int64                        `json:"revision"`
	ThreadID           string                       `json:"thread_id,omitempty"`
	Generation         int64                        `json:"generation"`
	ToolsetHash        string                       `json:"toolset_hash,omitempty"`
	Preparing          bool                         `json:"preparing"`
	Maintenance        *ConversationMaintenanceView `json:"maintenance,omitempty"`
	Pending            bool                         `json:"pending"`
	LastActivityMillis int64                        `json:"last_activity_millis"`
	Summary            *ConversationSummaryView     `json:"summary,omitempty"`
}

// ConversationSummaryView describes summary coverage without exposing its body.
type ConversationSummaryView struct {
	ID                  string `json:"id"`
	SourceThreadID      string `json:"source_thread_id"`
	Version             int64  `json:"version"`
	MaxSeq              int64  `json:"max_seq"`
	LastCompletedTurnID string `json:"last_completed_turn_id,omitempty"`
	CreatedAtMillis     int64  `json:"created_at_millis"`
	Current             bool   `json:"current"`
}

// SetConversationHandoffSettingsPayload sets the idle summary override.
type SetConversationHandoffSettingsPayload struct {
	IdleMinutes int64 `json:"idle_minutes"`
}

// ConversationHandoffPayload queues summary preparation or a fresh thread.
type ConversationHandoffPayload struct {
	PrepareOnly bool `json:"prepare_only"`
}

// ConversationMaintenanceView describes the durable owner maintenance delivery.
// RequestID is the original owner request, not the dispatch child request.
type ConversationMaintenanceView struct {
	RequestID string `json:"request_id"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}
