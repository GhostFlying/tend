package protocol

import (
	"net/url"
	"strconv"
)

// IssueContextQuery preserves the legacy source position separately from the
// opt-in event cursor. A cursor is a read position, never source acknowledgement.
type IssueContextQuery struct {
	IssueID     string `json:"issue_id,omitempty"`
	After       string `json:"after,omitempty"`
	AfterSource *int64 `json:"after_source,omitempty"`
	View        string `json:"view,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func NormalizeIssueContextQuery(q IssueContextQuery) (IssueContextQuery, error) {
	if q.View != "" && q.View != "incremental" {
		return q, Errorf(CodeInvalidRequest, "view must be incremental or omitted")
	}
	if q.View == "incremental" {
		if q.After != "" || q.AfterSource != nil {
			return q, Errorf(CodeInvalidRequest, "incremental view uses cursor, not after or after_source")
		}
		if q.Limit == 0 {
			q.Limit = 50
		}
		if q.Limit < 1 || q.Limit > 100 {
			return q, Errorf(CodeInvalidRequest, "limit must be between 1 and 100")
		}
	} else {
		if q.Cursor != "" || q.Limit != 0 {
			return q, Errorf(CodeInvalidRequest, "cursor and limit require view=incremental")
		}
		if q.After != "" {
			n, err := strconv.ParseInt(q.After, 10, 64)
			if err != nil || n < 0 {
				return q, Errorf(CodeInvalidRequest, "after must be a nonnegative source sequence string; use after_source")
			}
			if q.AfterSource != nil && *q.AfterSource != n {
				return q, Errorf(CodeInvalidRequest, "after and after_source disagree; use one source position")
			}
			q.AfterSource = &n
		}
		if q.AfterSource == nil {
			n := int64(0)
			q.AfterSource = &n
		}
		if *q.AfterSource < 0 {
			return q, Errorf(CodeInvalidRequest, "invalid source cursor")
		}
	}
	return q, nil
}

// ParseIssueContextQuery is shared by HTTP and native CLI. The owner API's
// historical after query remains a supported source-position alias.
func ParseIssueContextQuery(v url.Values) (IssueContextQuery, error) {
	q := IssueContextQuery{}
	for key, values := range v {
		if len(values) != 1 {
			return q, Errorf(CodeInvalidRequest, "context query parameters must occur once")
		}
		switch key {
		case "after":
			q.After = values[0]
		case "after_source":
			n, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return q, Errorf(CodeInvalidRequest, "invalid source cursor")
			}
			q.AfterSource = &n
		case "view":
			q.View = values[0]
		case "cursor":
			q.Cursor = values[0]
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 {
				return q, Errorf(CodeInvalidRequest, "limit must be between 1 and 100")
			}
			q.Limit = n
		default:
			return q, Errorf(CodeInvalidRequest, "unknown context query parameter")
		}
	}
	return NormalizeIssueContextQuery(q)
}

// IssueContextIncremental reports current object versions selected by a fixed
// event interval. Values are current at read time, not reconstructed historical
// values at through. Later events remain readable in the following interval.
type IssueContextIncremental struct {
	IssueID         string               `json:"issue_id"`
	ContextRevision int64                `json:"context_revision"`
	Kind            string               `json:"kind"`
	From            int64                `json:"from"`
	Through         int64                `json:"through"`
	Items           []IssueContextChange `json:"items"`
	More            bool                 `json:"more"`
	NextCursor      string               `json:"next_cursor"`
	ContentID       string               `json:"content_id"`
}

type IssueContextChange struct {
	Kind        string                 `json:"kind"`
	ID          string                 `json:"id"`
	EventSeq    int64                  `json:"event_seq,omitempty"`
	Revision    int64                  `json:"revision,omitempty"`
	State       string                 `json:"state,omitempty"`
	Removed     bool                   `json:"removed,omitempty"`
	Working     *IssueContextWorking   `json:"working,omitempty"`
	Task        *IssueTaskSummary      `json:"task,omitempty"`
	Observation *IssueDeliveryView     `json:"observation,omitempty"`
	Delivery    *IssueContextDelivery  `json:"delivery,omitempty"`
	Review      *IssueContextReviewRef `json:"review,omitempty"`
	Attention   *AttentionView         `json:"attention,omitempty"`
	Fact        *IssueMaterialFact     `json:"fact,omitempty"`
	Source      *IssueContextSource    `json:"source,omitempty"`
}

// IssueContextWorking contains current responsibility and evidence indexes,
// rather than old report bodies or superseded candidate explanations.
type IssueContextWorking struct {
	Issue    Issue          `json:"issue"`
	Progress *IssueProgress `json:"progress,omitempty"`
}
type IssueContextDelivery struct {
	DutyID          string                  `json:"duty_id"`
	Revision        int64                   `json:"revision"`
	Status          string                  `json:"status"`
	SourceTaskID    string                  `json:"source_task_id,omitempty"`
	ExecutionTaskID string                  `json:"execution_task_id,omitempty"`
	Candidate       *IssueDeliveryCandidate `json:"candidate,omitempty"`
}
type IssueContextReviewRef struct {
	ReviewID string `json:"review_id"`
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
}

// Source indexes retain source/version/state and original file identities.
// Fetch the message or materials source for its complete text.
type IssueContextSource struct {
	MessageID           string                   `json:"message_id"`
	NeedsReview         bool                     `json:"needs_review"`
	Admitted            bool                     `json:"admitted"`
	Seq                 int64                    `json:"seq"`
	Author              string                   `json:"author"`
	ContentRevision     int64                    `json:"content_revision"`
	AssociationRevision int64                    `json:"association_revision"`
	Linked              bool                     `json:"linked"`
	Withdrawn           bool                     `json:"withdrawn"`
	Disposition         string                   `json:"disposition,omitempty"`
	State               MessageState             `json:"state,omitempty"`
	Attachments         []ConversationAttachment `json:"attachments,omitempty"`
	Images              []ConversationImage      `json:"images,omitempty"`
}
