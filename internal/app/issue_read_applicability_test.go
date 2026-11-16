package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueReadApplicabilityFollowsNormalCloseCancelAndReopen(t *testing.T) {
	for _, end := range []string{"close", "cancel"} {
		t.Run(end, func(t *testing.T) {
			h := newHarness(t)
			issue := createBacklogTestIssue(t, h)
			if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "progress-owner"}, protocol.PutAgentCategoryPayload{Title: "Progress owner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue = issueAdmissionStatus(t, h, issue, protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "progress-owner"})
			issue = issueAdmissionStatus(t, h, issue, protocol.IssueInProgress, nil)
			target := protocol.Target{Type: "issue", ID: issue.IssueID}
			p := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Installation has not started", NextStep: "Install the selected release"}
			if op := h.exec(owner, h.env("issues.update_progress", target, p, rev(issue.Revision)), UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			progressRevision := issue.Revision
			info := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "information", Title: "Earlier installation hint"})
			read := func(want protocol.IssueWorkStatus, historical bool, reason string) {
				t.Helper()
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					v, err := views.CanonicalIssue(t.Context(), tx.Q(), issue.IssueID, 0)
					if err != nil {
						return err
					}
					if v.EffectiveWorkStatus != want || v.Progress == nil || v.Progress.Historical != historical || v.Progress.HistoricalReason != reason || v.Progress.IssueRevision != progressRevision || v.Progress.Summary != p.Summary {
						t.Fatalf("wrong effective/current history projection: %+v progress=%+v", v, v.Progress)
					}
					a, err := views.Attention(t.Context(), tx.Q(), info.AttentionID)
					if err != nil {
						return err
					}
					if a.Status != "open" || a.Historical != historical {
						t.Fatalf("information original/applicability changed: %+v", a)
					}
					page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{IncludeClosed: true, Limit: 10})
					if err != nil {
						return err
					}
					if len(page.Items) != 1 || page.Items[0].EffectiveWorkStatus != want {
						t.Fatalf("list lost effective terminal state: %+v", page)
					}
					raw, err := store.IssueProgress(t.Context(), tx.Q(), issue.IssueID, issue.Revision)
					if err != nil {
						return err
					}
					if raw.Historical || raw.HistoricalReason != "" || raw.Summary != p.Summary {
						t.Fatal("read rewrote original progress", raw)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			read(protocol.IssueInProgress, false, "")
			h.clock.Advance(time.Millisecond)
			if end == "close" {
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)))
			} else {
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "No pending work"}}))
			}
			expected := protocol.IssueDone
			if end == "cancel" {
				expected = protocol.IssueWorkCancelled
			}
			read(expected, true, "issue_ended")
			endedRevision := issue.Revision
			h.clock.Advance(time.Millisecond)
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Explicit new work"}))
			read(protocol.IssueBacklog, true, "issue_reopened")
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				past, err := views.CanonicalIssue(t.Context(), tx.Q(), issue.IssueID, progressRevision)
				if err != nil {
					return err
				}
				if past.Progress.Historical || !past.Historical || past.Progress.IssueRevision != progressRevision {
					t.Fatalf("current endpoint polluted old snapshot: %+v", past)
				}
				ended, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, endedRevision)
				if err == nil && ended.State != map[string]protocol.IssueState{"close": protocol.IssueClosed, "cancel": protocol.IssueCancelled}[end] {
					t.Fatal("read reopened original endpoint", ended)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
