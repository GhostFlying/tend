package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHandoffOnlyIgnoresExactAbandonedStartingDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, turnStatus string
		outcome          DeliveryOutcome
		wantActive       bool
	}{{"lost_start_closed_session", store.TurnStarting, Unknown, false}, {"live_start", store.TurnStarting, 0, true}, {"started_remains_active", store.TurnStarted, Unknown, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
				t.Fatal(err)
			}
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
			h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "original uncertain message"}, nil), SubmitMessage{})
			d, _, ok, err := h.app.NextDelivery(ctx, MainThreadDestination)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			var messageID string
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(ctx, `SELECT message_id FROM conversation_messages WHERE author='owner'`).Scan(&messageID)
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: d.RequestID, MessageID: messageID, ThreadID: "source", Status: tc.turnStatus}); err != nil {
				t.Fatal(err)
			}
			if tc.outcome != 0 {
				if err := h.app.FinishDelivery(ctx, d, DeliveryResult{Outcome: tc.outcome, Reason: "turn/start response lost; old session was closed"}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := h.app.HandoffSnapshot(ctx, "main")
			if err != nil || before.Active != tc.wantActive {
				t.Fatalf("activity %+v %v", before, err)
			}
			if before.Pending != (tc.outcome == 0) {
				t.Fatalf("unknown counted as running or live delivery omitted: %+v", before)
			}
			if tc.outcome == Unknown && tc.turnStatus == store.TurnStarting {
				input, err := h.app.HandoffSummaryInput(ctx, "main", 256<<10)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(input.Observations), `"state":"unknown"`) || !strings.Contains(string(input.Observations), "original uncertain message") {
					t.Fatalf("unknown reference state lost: %s", input.Observations)
				}
				claimed, err := h.app.ClaimSummaryAttempt(ctx, input.Snapshot, 1000)
				if err != nil || !claimed {
					t.Fatalf("historical unknown blocked summary %v %v", claimed, err)
				}
				summary, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: input.Snapshot, Body: "Original message outcome remains unknown; do not repeat the action.", FormatVersion: 2})
				if err != nil {
					t.Fatal(err)
				}
				latest, err := h.app.HandoffSnapshot(ctx, "main")
				if err != nil || !summary.Snapshot.Covers(latest) {
					t.Fatalf("unknown reference facts could not be current %+v %v", latest, err)
				}
			}
			job, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "source", SourceGeneration: 1, SourceVersion: before.Version, ToolsetHash: "next", Prompt: "reference preserves unknown original message"})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.RecordHandoffTarget(ctx, job.ID, "target", "target.rollout"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.AdvanceHandoff(ctx, job.ID, "created", "injecting"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.AdvanceHandoff(ctx, job.ID, "injecting", "injected"); err != nil {
				t.Fatal(err)
			}
			_, err = h.app.CommitHandoff(ctx, job.ID)
			if tc.wantActive {
				if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
					t.Fatalf("live turn replaced: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			original, ok, err := h.app.MainTurn(ctx, d.RequestID)
			if err != nil || !ok || original.Status != tc.turnStatus || original.TurnID != "" {
				t.Fatalf("original outcome fabricated: %+v %v", original, err)
			}
			var status string
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(ctx, `SELECT status FROM outbox WHERE request_id=?`, d.RequestID).Scan(&status)
			}); err != nil {
				t.Fatal(err)
			}
			expected := "sending"
			if tc.outcome == Unknown {
				expected = "unknown"
			}
			if status != expected {
				t.Fatalf("delivery replayed/rewritten: %s", status)
			}
		})
	}
}

func TestHandoffActivityIsThreadScopedAndMissingEvidenceConservative(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "message", Author: "owner", Body: "original"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: "orphan", MessageID: "message", ThreadID: "unrelated", Status: store.TurnStarting}
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil || snap.Active {
		t.Fatalf("other thread blocked source %+v %v", snap, err)
	}
	turn.ThreadID = "source"
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	snap, err = h.app.HandoffSnapshot(ctx, "main")
	if err != nil || !snap.Active {
		t.Fatalf("missing delivery evidence assumed stopped %+v %v", snap, err)
	}
}

func TestRetargetPreparedHandoffOnlyBeforeExternalCreation(t *testing.T) {
	for _, phase := range []string{"prepared", "creating", "created", "injecting", "injected", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
				t.Fatal(err)
			}
			snapshot, err := h.app.HandoffSnapshot(ctx, "main")
			if err != nil {
				t.Fatal(err)
			}
			job, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "source", SourceGeneration: 1, SourceVersion: snapshot.Version, ToolsetHash: "previous-upgrade", Prompt: "old prompt"})
			if err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				if err := h.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "unknown" {
				if err := h.app.AdvanceHandoff(ctx, job.ID, "creating", "unknown"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "created" || phase == "injecting" || phase == "injected" {
				if err := h.app.RecordHandoffTarget(ctx, job.ID, "confirmed-target", "target.rollout"); err != nil {
					t.Fatal(err)
				}
				if phase != "created" {
					if err := h.app.AdvanceHandoff(ctx, job.ID, "created", "injecting"); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "injected" {
					if err := h.app.AdvanceHandoff(ctx, job.ID, "injecting", "injected"); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = h.app.RetargetPreparedHandoff(ctx, job.ID, "current-toolset", snapshot.Version, "new prompt")
			got, _, readErr := h.app.GetHandoff(ctx, job.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if phase != "prepared" {
				if protocol.CodeOf(err) != protocol.CodeInvalidTransition || got.ToolsetHash != "previous-upgrade" || got.Phase != phase {
					t.Fatalf("external intent retargeted %+v %v", got, err)
				}
				return
			}
			if err != nil || got.ToolsetHash != "current-toolset" || got.Prompt != "new prompt" || got.Phase != "prepared" || got.TargetThreadID != "" {
				t.Fatalf("retarget failed %+v %v", got, err)
			}
			h.clock.Advance(time.Minute)
			if err := h.app.RetargetPreparedHandoff(ctx, job.ID, "current-toolset", snapshot.Version, "new prompt"); err != nil {
				t.Fatal(err)
			}
			replay, _, _ := h.app.GetHandoff(ctx, job.ID)
			if replay != got {
				t.Fatalf("exact replay mutated intent before=%+v after=%+v", got, replay)
			}
			if err := h.store.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "new-observation", Author: "system", Body: "new notice"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.app.RetargetPreparedHandoff(ctx, job.ID, "latest", snapshot.Version, "stale"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatalf("stale snapshot accepted %v", err)
			}
			snap, err := h.app.HandoffSnapshot(ctx, "main")
			if err != nil {
				t.Fatal(err)
			}
			if err := h.app.RetargetPreparedHandoff(ctx, job.ID, "latest", snap.Version, "fresh"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.SetMainThread(ctx, "main", "different"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.RetargetPreparedHandoff(ctx, job.ID, "next", snap.Version, "wrong source"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatalf("source mapping guard absent %v", err)
			}
		})
	}
}
