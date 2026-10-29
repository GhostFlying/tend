package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedReparentFencesClaimedRejectedManagerDelivery(t *testing.T) {
	for _, status := range []string{"pending", "sending", "unknown"} {
		t.Run(status, func(t *testing.T) {
			h := hostedManagerHarness(t)
			root := createCanonicalOwnerTestIssue(t, h)
			child := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			attach := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}, ParentIssueID: root.IssueID, ExpectedParentRevision: rev(root.Revision)}
			child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", attach))
			destination := createCanonicalOwnerTestIssue(t, h)
			turn, _ := startHostedIssueReview(t, h, child)
			turn.Status, turn.TurnID = store.TurnRejected, ""
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			deliveries, err := h.app.InFlightDeliveries(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var original Delivery
			for _, d := range deliveries {
				if d.RequestID == turn.RequestID {
					original = d
				}
			}
			if original.RequestID == "" {
				t.Fatal("original manager delivery was not claimed")
			}
			if err := h.app.FinishDelivery(t.Context(), original, DeliveryResult{Outcome: Retry, RetryAfter: time.Second}); err != nil {
				t.Fatal(err)
			}
			if status != "pending" {
				h.clock.Advance(time.Second)
				d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+child.IssueID)
				if err != nil || !ok || d.RequestID != turn.RequestID {
					t.Fatal(d, ok, err)
				}
				// Manager.Deliver reads AgentContext here, before it writes TurnStarting.
				// Exercise the same persisted window without scheduling a native request.
				if _, err := h.app.AgentContext(t.Context(), "issue:"+child.IssueID, turn.RequestID); err != nil {
					t.Fatal(err)
				}
				if status == "unknown" {
					if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: root.AssignmentEpoch}, ParentIssueID: destination.IssueID, ExpectedParentRevision: rev(destination.Revision)}
			op := canonicalLifecycleControl(t, h, root, "reparent", p)
			if status == "pending" {
				next := canonicalLifecycleIssue(t, op)
				if next.ParentIssueID != destination.IssueID {
					t.Fatal(next)
				}
			} else {
				expectWorkCode(t, op, protocol.CodeInvalidTransition)
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					saved, err := store.GetCanonicalIssue(t.Context(), tx.Q(), root.IssueID)
					if err == nil && (saved.Revision != root.Revision || saved.ParentIssueID != "") {
						t.Fatal("rejected reparent changed inherited context", saved)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if status == "sending" {
					// A definite failed delivery settles the actual claimed
					// operation; the owner can then reparent normally.
					claimed, err := h.app.InFlightDeliveries(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					for _, d := range claimed {
						if d.RequestID == turn.RequestID {
							if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Failed}); err != nil {
								t.Fatal(err)
							}
						}
					}
					next := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, root, "reparent", p))
					if next.ParentIssueID != destination.IssueID {
						t.Fatal(next)
					}
				}
			}
		})
	}
}

func TestHostedReparentFencesFirstManagerDeliveryBeforeNativeAdmission(t *testing.T) {
	for _, kind := range []string{"message", "event"} {
		for _, status := range []string{"pending", "sending", "unknown", "failed"} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				h := hostedManagerHarness(t)
				root := createCanonicalOwnerTestIssue(t, h)
				child := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
				attach := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}, ParentIssueID: root.IssueID, ExpectedParentRevision: rev(root.Revision)}
				child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", attach))
				destination := createCanonicalOwnerTestIssue(t, h)
				conversation := "issue:" + child.IssueID
				if kind == "message" {
					op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Review this newly delegated responsibility"}, nil), SubmitMessage{})
					if op.Status != protocol.OpAccepted {
						t.Fatal(op)
					}
				}
				if status != "pending" {
					d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
					if err != nil || !ok {
						t.Fatal(d, ok, err)
					}
					want := OpHandleEvent
					if kind == "message" {
						want = OpHandleMessage
					}
					if d.Operation != want {
						t.Fatalf("delivery %s, want %s", d.Operation, want)
					}
					if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
						t.Fatal(err)
					}
					// This is the first Deliver: context was read at the injection seam before
					// PutMainTurn, so no native/logical execution row can anchor the fence.
					if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
						var n int
						err := tx.Q().QueryRowContext(t.Context(), `SELECT
       (SELECT COUNT(*) FROM hosted_executions WHERE execution_id=?)+
       (SELECT COUNT(*) FROM main_turns WHERE request_id=?)+
       (SELECT COUNT(*) FROM logical_turn_executions WHERE request_id=?)`, d.RequestID, d.RequestID, d.RequestID).Scan(&n)
						if err == nil && n != 0 {
							t.Fatal("fixture already admitted native execution", n)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if status == "unknown" || status == "failed" {
						outcome := Unknown
						if status == "failed" {
							outcome = Failed
						}
						if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: outcome}); err != nil {
							t.Fatal(err)
						}
					}
				}
				p := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: root.AssignmentEpoch}, ParentIssueID: destination.IssueID, ExpectedParentRevision: rev(destination.Revision)}
				op := canonicalLifecycleControl(t, h, root, "reparent", p)
				if status == "sending" || status == "unknown" {
					expectWorkCode(t, op, protocol.CodeInvalidTransition)
				} else {
					next := canonicalLifecycleIssue(t, op)
					if next.ParentIssueID != destination.IssueID {
						t.Fatal(next)
					}
				}
			})
		}
	}
}
