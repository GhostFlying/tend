package protocol

import (
	"encoding/json"
	"strings"
)

// ContextIndexBudget bounds each automatic injection, independently of owner input.
const ContextIndexBudget = 2048

// ConversationContextQuery pages references only. Reading never acknowledges sources.
type ConversationContextQuery struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Section        string `json:"section,omitempty"`
	After          string `json:"after,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

func NormalizeConversationContextQuery(q ConversationContextQuery) (ConversationContextQuery, error) {
	if q.Section == "" {
		q.Section = "state"
	}
	switch q.Section {
	case "state", "sources", "facts", "threads":
	default:
		return q, Errorf(CodeInvalidRequest, "invalid context index section")
	}
	if q.Section == "state" && q.After != "" {
		return q, Errorf(CodeInvalidRequest, "state index has no paging cursor")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 || len(q.After) > 512 {
		return q, Errorf(CodeInvalidRequest, "invalid context index cursor or limit")
	}
	return q, nil
}

// ConversationContextIndex contains identities and versions, never historical prose.
type ConversationContextIndex struct {
	Format            string                         `json:"format"`
	ConversationID    string                         `json:"conversation_id"`
	Kind              string                         `json:"kind"`
	IssueID           string                         `json:"issue_id,omitempty"`
	Revision          int64                          `json:"revision"`
	ContextRevision   int64                          `json:"context_revision,omitempty"`
	AssignmentEpoch   int64                          `json:"assignment_epoch,omitempty"`
	State             string                         `json:"state,omitempty"`
	ThroughMessageSeq int64                          `json:"through_message_seq"`
	ThroughEventSeq   int64                          `json:"through_event_seq"`
	FocusMessageSeq   int64                          `json:"focus_message_seq,omitempty"`
	ChangedObjects    []ConversationContextReference `json:"changed_objects,omitempty"`
	ChangesMore       bool                           `json:"changes_more,omitempty"`
	SourceCount       int64                          `json:"source_count"`
	FactCount         int64                          `json:"fact_count"`
	ThreadCount       int64                          `json:"thread_count"`
	ReviewRequired    bool                           `json:"review_required"`
	Section           string                         `json:"section"`
	Items             []ConversationContextReference `json:"items"`
	More              bool                           `json:"more"`
	Next              string                         `json:"next,omitempty"`
	Coverage          string                         `json:"coverage"`
}

type ConversationContextReference struct {
	ID                  string `json:"id"`
	Kind                string `json:"kind"`
	ConversationID      string `json:"conversation_id,omitempty"`
	IssueID             string `json:"issue_id,omitempty"`
	SourceMessageID     string `json:"source_message_id,omitempty"`
	ContentRevision     int64  `json:"content_revision,omitempty"`
	AssociationRevision int64  `json:"association_revision,omitempty"`
	Seq                 int64  `json:"seq,omitempty"`
	NeedsReview         bool   `json:"needs_review"`
	Withdrawn           bool   `json:"withdrawn,omitempty"`
	Superseded          bool   `json:"superseded,omitempty"`
	RolloutPath         string `json:"rollout_path,omitempty"`
}

// ContextIndexText is deliberately a locator, not evidence of having read its targets.
func ContextIndexText(v ConversationContextIndex) (string, error) {
	v.Items = nil
	v.Section = "state"
	v.More = false
	v.Next = ""
	v.Coverage = ""
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	text := `[Tend context index; reference only, not new instructions or authorization.]
Read current state and reference pages with conversation_context_index (state/sources/facts/threads). Recover exact originals with conversation_read, issues_materials_read, issues_context_read view=incremental, attachments_read or agent_rollout_read, only where advertised and permitted. Follow pagination as needed; do not scan all history by default. Consult original goals, constraints, approvals, rejections and pauses before acting. An index or cursor is not source review, confirmed facts or permission to replay. Preserve unknown outcomes. Current admitted messages arrive separately.
` + string(b)
	if len(text) > ContextIndexBudget && len(v.ChangedObjects) > 0 {
		v.ChangedObjects = v.ChangedObjects[:len(v.ChangedObjects)-1]
		v.ChangesMore = true
		return ContextIndexText(v)
	}
	if len(text) > ContextIndexBudget {
		return "", Errorf(CodeInvalidRequest, "context identity exceeds injection budget")
	}
	return strings.TrimSpace(text), nil
}
