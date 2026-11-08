package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueContextIncrementalSuppliesActualManagerReviewTuple(t *testing.T) {
	f := newHistoricalHostedDelivery(t)
	var lastCursor string
	read := func() protocol.IssueContextReview {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"view": "incremental", "limit": 100})
		r := f.e.svc.Handle(t.Context(), Call{Principal: f.principal, Native: "incremental-manager-context-read", TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "issues_context_read", Arguments: args})
		var a answerView
		if json.Unmarshal([]byte(r.Text), &a) != nil || !a.OK {
			t.Fatal(r)
		}
		var page struct {
			ContextRevision int64  `json:"context_revision"`
			More            bool   `json:"more"`
			NextCursor      string `json:"next_cursor"`
			Items           []struct {
				Working *protocol.IssueContextWorking `json:"working"`
				Source  *struct {
					MessageID           string `json:"message_id"`
					ContentRevision     int64  `json:"content_revision"`
					AssociationRevision int64  `json:"association_revision"`
					NeedsReview         bool   `json:"needs_review"`
					Admitted            bool   `json:"admitted"`
				} `json:"source"`
			} `json:"items"`
		}
		if err := json.Unmarshal(a.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.ContextRevision < 1 || page.More {
			t.Fatalf("incremental read cannot construct an exact manager review: %s", a.Result)
		}
		p := protocol.IssueContextReview{ContextRevision: page.ContextRevision, Summary: "Reviewed actual admitted sources; queued changes remain normal future input"}
		lastCursor = page.NextCursor
		for _, item := range page.Items {
			if item.Working != nil && item.Working.Issue.IssueID == f.issue.IssueID {
				p.ExpectedAssignmentEpoch = item.Working.Issue.AssignmentEpoch
			}
			if s := item.Source; s != nil && s.NeedsReview && s.Admitted {
				p.Sources = append(p.Sources, protocol.LogicalTurnReview{MessageID: s.MessageID, ContentRevision: s.ContentRevision, AssociationRevision: s.AssociationRevision})
			}
		}
		if p.ExpectedAssignmentEpoch < 1 {
			t.Fatal("incremental snapshot omitted current responsibility epoch")
		}
		return p
	}
	review := read()
	if len(review.Sources) == 0 {
		t.Fatal("admitted current manager source obligation missing")
	}
	// A new queued source changes context revision without becoming an admitted
	// instruction or taking over this manager turn's review responsibility.
	f.execute(t, f.owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + f.issue.IssueID, Body: "Later owner correction still queued"}, nil)
	call := func(seq int64, p protocol.IssueContextReview) answerView {
		t.Helper()
		raw, _ := json.Marshal(p)
		args, _ := json.Marshal(hostedMutationArgs{EffectSeq: seq, Payload: raw})
		r := f.e.svc.Handle(t.Context(), Call{Principal: f.principal, Native: "incremental-manager-review-" + string(rune('a'+seq)), RequestID: f.ids.New(protocol.PrefixRequest), TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "issues_context_review", Arguments: args})
		var a answerView
		if err := json.Unmarshal([]byte(r.Text), &a); err != nil {
			t.Fatal(r, err)
		}
		return a
	}
	stale := call(1, review)
	if stale.OK || stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("incremental cursor bypassed original context CAS", stale)
	}
	current := read()
	if current.ContextRevision <= review.ContextRevision {
		t.Fatal("context revision not current", current, review)
	}
	done := call(2, current)
	if !done.OK {
		t.Fatal("exact incremental tuple cannot complete real hosted review", done)
	}
	var result struct {
		Outcome struct {
			Complete bool `json:"complete"`
		} `json:"outcome"`
	}
	if json.Unmarshal(done.Result, &result) != nil || !result.Outcome.Complete {
		t.Fatal("queued source became mandatory current review", done)
	}
	raw, _ := json.Marshal(map[string]any{"view": "incremental", "cursor": lastCursor, "limit": 100})
	updated := f.e.svc.Handle(t.Context(), Call{Principal: f.principal, Native: "incremental-manager-after-review", TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "issues_context_read", Arguments: raw})
	var answer answerView
	var delta protocol.IssueContextIncremental
	if json.Unmarshal([]byte(updated.Text), &answer) != nil || !answer.OK || json.Unmarshal(answer.Result, &delta) != nil {
		t.Fatal(updated)
	}
	sawReviewed := false
	for _, v := range delta.Items {
		if v.Source != nil && v.Source.MessageID == f.turn.MessageID {
			sawReviewed = v.Source.Admitted && !v.Source.NeedsReview
		}
	}
	if !sawReviewed {
		t.Fatal("actual review completion did not update source obligation in delta", delta)
	}

}
