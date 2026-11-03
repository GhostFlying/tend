package protocol

// HandoffAuditIdentity is recorded when the event happens, never inferred from
// today's binding. Empty TargetThreadID means no target was confirmed yet.
type HandoffAuditIdentity struct {
	Format           int64  `json:"format"`
	Instance         string `json:"instance"`
	SourceThreadID   string `json:"source_thread_id"`
	SourceVersion    int64  `json:"source_version"`
	SourceGeneration int64  `json:"source_generation"`
	TargetThreadID   string `json:"target_thread_id,omitempty"`
	HandoffID        string `json:"handoff_id,omitempty"`
	Phase            string `json:"phase,omitempty"`
}

type HandoffAuditEvent struct {
	Sequence            int64                 `json:"sequence"`
	EventID             string                `json:"event_id"`
	ObjectID            string                `json:"object_id"`
	Kind                string                `json:"kind"`
	CreatedAtMillis     int64                 `json:"created_at_millis"`
	IdentityObservation string                `json:"identity_observation"`
	Identity            *HandoffAuditIdentity `json:"identity,omitempty"`
}

// This is a bounded retained event page, not a lifetime count or provider queue.
type HandoffAuditPage struct {
	Instance        string              `json:"instance"`
	Items           []HandoffAuditEvent `json:"items"`
	NextAfter       int64               `json:"next_after"`
	More            bool                `json:"more"`
	HistoryComplete bool                `json:"history_complete"`
}
