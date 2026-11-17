package protocol

import "encoding/json"

// ManagerHistoryRead is a bounded observation of an original stopped Run, not a
// scheduled action or authority to resume native execution.
type ManagerHistoryRead struct {
	ID             string          `json:"id"`
	RunID          string          `json:"run_id"`
	ConversationID string          `json:"conversation_id"`
	Execution      RunExecutionPin `json:"execution"`
	ThreadID       string          `json:"thread_id"`
	MessageID      string          `json:"message_id"`
	TurnID         string          `json:"turn_id"`
}
type ManagerHistoryReply struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}
