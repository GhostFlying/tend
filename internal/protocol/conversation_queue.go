package protocol

// MessageControl separates queue admission from native turn outcomes.
type MessageControl struct {
	Revision    int64 `json:"revision"`
	WithdrawnAt int64 `json:"withdrawn_at,omitempty"`
}

// ConversationQueueEntry is a server-owned queue position, not a local send.
type ConversationQueueEntry struct {
	Item        ConversationItem `json:"item"`
	Position    int64            `json:"position"`
	Revision    int64            `json:"revision"`
	Status      string           `json:"status"`
	CanWithdraw bool             `json:"can_withdraw"`
}

type ConversationQueuePage struct {
	Enabled  bool                     `json:"enabled"`
	EventSeq int64                    `json:"event_seq"`
	Items    []ConversationQueueEntry `json:"items"`
	Total    int64                    `json:"total"`
	Next     int64                    `json:"next,omitempty"`
}

type WithdrawMessagePayload struct{}

type WithdrawMessageResult struct {
	MessageID string         `json:"message_id"`
	Control   MessageControl `json:"control"`
}

// TranscriptBlock groups a trigger with the output of its own turn.
type TranscriptBlock struct {
	NoticeOnly bool               `json:"notice_only,omitempty"`
	Position   int64              `json:"position"`
	Trigger    ConversationItem   `json:"trigger"`
	Items      []ConversationItem `json:"items"`
	More       bool               `json:"more"`
	NextSeq    int64              `json:"next_seq,omitempty"`
}

type TranscriptPage struct {
	Blocks     []TranscriptBlock `json:"blocks"`
	More       bool              `json:"more"`
	NextBefore int64             `json:"next_before,omitempty"`
	EventSeq   int64             `json:"event_seq"`
}
