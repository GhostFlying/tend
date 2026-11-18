package app

import (
	"encoding/json"
	"testing"

	nativeharness "github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueOwnerSteerPendingNativeFences(t *testing.T) {
	for _, fence := range []string{"revocation", "withdrawal", "started before revocation"} {
		t.Run(fence, func(t *testing.T) {
			ctx := t.Context()
			h, _, issue, as, _ := reviewManagerAssignment(t)
			turn, _ := startHostedIssueReview(t, h, issue)
			old := queueMessage(t, h, "Previously authorized operation")
			linkIssueSteerSource(t, h, issue.IssueID, old)
			earlier := steerOperation(t, h.exec(owner, urgentTestEnvelope(t, h, issue.IssueID, as.RunID, old, ""), SteerIssueOwner{}), "queued")
			d, err := h.app.ClaimIssueOwnerSteer(ctx, turn.RequestID, as.RunID, turn.ThreadID, turn.TurnID, true)
			if err != nil || d.MessageID != earlier.MessageID {
				t.Fatal(d, err)
			}
			raw, err := json.Marshal(coordination.Request{ThreadID: turn.ThreadID, TurnID: turn.TurnID,
				Input: nativeharness.TurnInput{Text: d.Text, UserMessageID: d.MessageID}})
			if err != nil {
				t.Fatal(err)
			}
			original, err := h.app.QueueManagerNative(ctx, as.RunID, "steer", raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				var state string
				if err := tx.Q().QueryRowContext(ctx, "SELECT status FROM manager_native_requests WHERE request_id=?", original).Scan(&state); err != nil {
					return err
				}
				if state != "pending" {
					t.Fatalf("expected unclaimed native request, got %s", state)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if fence == "started before revocation" {
				op := h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
				var got protocol.ManagerNativeRequest
				if err := json.Unmarshal(op.Result, &got); err != nil || got.ID != original {
					t.Fatalf("could not establish the original claim: %+v, %v", op, err)
				}
			}
			if fence != "withdrawal" {
				newer := queueMessage(t, h, "Withdraw the previously authorized operation")
				linkIssueSteerSource(t, h, issue.IssueID, newer)
				steerOperation(t, h.exec(owner, urgentTestEnvelope(t, h, issue.IssueID, as.RunID, newer, old), SteerIssueOwner{}), "queued")
			} else {
				withdrawOwnerSource(t, h, old)
			}
			op := h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			var got protocol.ManagerNativeRequest
			if err := json.Unmarshal(op.Result, &got); err != nil {
				t.Fatal(err)
			}
			reply, ready, err := h.app.ManagerNativeResult(ctx, as.RunID, original)
			if fence == "started before revocation" {
				if err != nil || ready || got.ID != original {
					t.Fatalf("started native attempt must retain the original unsettled request: %+v, %+v, %v", got, reply, err)
				}
				return
			}
			if got.ID == original {
				t.Fatalf("runner was offered original pending steer after %s", fence)
			}
			if err != nil || !ready || !reply.Rejected || reply.ID != original {
				t.Fatalf("missing definite no-dispatch receipt: %+v, %v, %v", reply, ready, err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				record, err := store.GetIssueOwnerSteer(ctx, tx.Q(), earlier.MessageID)
				if err != nil {
					return err
				}
				want := "source_changed"
				if fence == "revocation" {
					want = "source_revoked"
				}
				if record.Status != "rejected" || record.Reason != want || record.NativeRequestID != original || record.NativeItemID != "" {
					t.Fatalf("notice did not preserve the fenced original: %+v", record)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssueOwnerSteerLateAcceptanceOnOriginalActiveTurn(t *testing.T) {
	ctx := t.Context()
	h, _, issue, as, _ := reviewManagerAssignment(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	original := queueMessage(t, h, "An urgent update")
	linkIssueSteerSource(t, h, issue.IssueID, original)
	env := urgentTestEnvelope(t, h, issue.IssueID, as.RunID, original, "")
	queued := steerOperation(t, h.exec(owner, env, SteerIssueOwner{}), "queued")
	d, err := h.app.ClaimIssueOwnerSteer(ctx, turn.RequestID, as.RunID, turn.ThreadID, turn.TurnID, true)
	if err != nil || d.MessageID != queued.MessageID {
		t.Fatal(d, err)
	}
	raw, err := json.Marshal(coordination.Request{ThreadID: turn.ThreadID, TurnID: turn.TurnID,
		Input: nativeharness.TurnInput{Text: d.Text, UserMessageID: d.MessageID}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.app.QueueManagerNative(ctx, as.RunID, "steer", raw)
	if err != nil {
		t.Fatal(err)
	}
	op := h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var claimed protocol.ManagerNativeRequest
	if err := json.Unmarshal(op.Result, &claimed); err != nil || claimed.ID != id {
		t.Fatal(op, err)
	}
	if err := h.app.RecordIssueOwnerSteerAnswer(ctx, d.MessageID, "unknown", "native_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	reply := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Method: "steer", Result: json.RawMessage("null")}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if reply.Status != protocol.OpSucceeded {
		t.Fatal(reply)
	}
	env.RequestID = h.ids.New(protocol.PrefixRequest)
	accepted := steerOperation(t, h.exec(owner, env, SteerIssueOwner{}), "accepted")
	if accepted.NativeRequestID != id || accepted.AcceptedAt == 0 || accepted.NativeItemID != "" || accepted.Reason != "waiting_safe_boundary" {
		t.Fatal("late acknowledgement was not reconciled against the exact original request", accepted)
	}
}
