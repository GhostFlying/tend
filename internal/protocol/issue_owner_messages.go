package protocol

// IssueMessageAssociation presents the existing source link without renaming
// opaque IDs or changing its author, correction lock or revision identity.
type IssueMessageAssociation struct {
	IssueID     string `json:"issue_id"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	OwnerLocked bool   `json:"owner_locked"`
}
type IssueMessageAssociations struct {
	MessageAssociationState
	Items []IssueMessageAssociation `json:"items"`
}
type IssueBackgroundBatch struct {
	BackgroundBatchState
	IssueID string `json:"issue_id,omitempty"`
}

// IssueConversationItem shares real message/media content with the historical
// facade. Choices retain original historical identities, never new questions.
type IssueConversationItem struct {
	Recovery       *AgentRecoveryView        `json:"recovery,omitempty"`
	ConversationID string                    `json:"conversation_id,omitempty"`
	Associations   *IssueMessageAssociations `json:"associations,omitempty"`
	ReturnLink     *ConversationReturnLink   `json:"return_link,omitempty"`
	Background     *IssueBackgroundBatch     `json:"background,omitempty"`
	Control        *MessageControl           `json:"control,omitempty"`
	Choices        []WorkItemChoice          `json:"choices,omitempty"`
	ConversationContent
}
type IssueConversationPage struct {
	Items []IssueConversationItem `json:"items"`
	More  bool                    `json:"more"`
}
type IssueAgentConversation struct {
	AgentConversationState
	IssueID string `json:"issue_id,omitempty"`
}
type IssueAgentConversationPage struct {
	RuntimeAvailable bool                     `json:"runtime_available"`
	Items            []IssueAgentConversation `json:"items"`
	Next             string                   `json:"next,omitempty"`
}

// Owner queue/transcript wrappers retain the original positions and control
// revisions while using the same canonical message projection as the detail.
type IssueConversationQueueEntry struct {
	Item        IssueConversationItem `json:"item"`
	Position    int64                 `json:"position"`
	Revision    int64                 `json:"revision"`
	Status      string                `json:"status"`
	CanWithdraw bool                  `json:"can_withdraw"`
}
type IssueConversationQueuePage struct {
	Active   *IssueConversationQueueEntry  `json:"active,omitempty"`
	Enabled  bool                          `json:"enabled"`
	EventSeq int64                         `json:"event_seq"`
	Items    []IssueConversationQueueEntry `json:"items"`
	Total    int64                         `json:"total"`
	Next     int64                         `json:"next,omitempty"`
}
type IssueTranscriptBlock struct {
	NoticeOnly bool                    `json:"notice_only,omitempty"`
	Position   int64                   `json:"position"`
	Trigger    IssueConversationItem   `json:"trigger"`
	Items      []IssueConversationItem `json:"items"`
	More       bool                    `json:"more"`
	NextSeq    int64                   `json:"next_seq,omitempty"`
}
type IssueTranscriptPage struct {
	Blocks     []IssueTranscriptBlock `json:"blocks"`
	More       bool                   `json:"more"`
	NextBefore int64                  `json:"next_before,omitempty"`
	EventSeq   int64                  `json:"event_seq"`
}
