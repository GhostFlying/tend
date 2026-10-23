package app

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRefreshHandoffKeepsTargetAndRequiresFreshInjection(t *testing.T) {
	for _, phase := range []string{"prepared", "creating", "created", "injecting", "injected", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.app.BindMainThread(ctx, "main", "source", "old", ""); err != nil {
				t.Fatal(err)
			}
			job, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "source", SourceGeneration: 1, ToolsetHash: "new", Prompt: "old reference"})
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
				if err := h.app.RecordHandoffTarget(ctx, job.ID, "target", "target.rollout"); err != nil {
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
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
			h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "new pending owner message"}, nil), SubmitMessage{})
			snap, err := h.app.HandoffSnapshot(ctx, "main")
			if err != nil {
				t.Fatal(err)
			}
			err = h.app.RefreshHandoff(ctx, job.ID, snap.Version, "new reference")
			allowed := phase == "prepared" || phase == "created" || phase == "injected"
			if !allowed {
				if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
					t.Fatalf("unconfirmed phase refreshed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := h.app.GetHandoff(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := phase
			if want == "injected" {
				want = "created"
			}
			if got.Phase != want || got.SourceVersion != snap.Version || got.Prompt != "new reference" {
				t.Fatalf("stale reference: %+v", got)
			}
			if phase != "prepared" && got.TargetThreadID != "target" {
				t.Fatal("target identity replaced")
			}
			if err := h.app.RefreshHandoff(ctx, job.ID, job.SourceVersion, "older"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatalf("version rolled back: %v", err)
			}
			if err := h.app.RefreshHandoff(ctx, job.ID, snap.Version+1, "future"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatalf("future version accepted: %v", err)
			}
			if phase == "injected" {
				if _, err := h.app.CommitHandoff(ctx, job.ID); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
					t.Fatalf("stale injection committed: %v", err)
				}
			}
			if err := h.app.SetMainThread(ctx, "main", "different"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.RefreshHandoff(ctx, job.ID, snap.Version, "changed mapping"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatalf("changed source refreshed: %v", err)
			}
		})
	}
}

func TestConversationMaintenanceTracksOriginalOwnerAndDurableOutcome(t *testing.T) {
	for _, outcome := range []DeliveryOutcome{Delivered, Failed, Unknown} {
		t.Run(string(rune(outcome+'0')), func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if got, err := h.app.ConversationMaintenance(ctx, "main"); err != nil || got != nil {
				t.Fatalf("initial %+v %v", got, err)
			}
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
			env := h.env("conversation.handoff", protocol.Target{Type: "conversation"}, protocol.ConversationHandoffPayload{PrepareOnly: true}, nil)
			h.exec(owner, env, ConversationHandoff{})
			got, err := h.app.ConversationMaintenance(ctx, "main")
			if err != nil || got.RequestID != env.RequestID || got.Status != "pending" {
				t.Fatalf("queued %+v %v", got, err)
			}
			d, _, ok, err := h.app.NextDelivery(ctx, MainThreadDestination)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			got, err = h.app.ConversationMaintenance(ctx, "main")
			if err != nil || got.Status != "sending" {
				t.Fatalf("running %+v %v", got, err)
			}
			if err := h.app.FinishDelivery(ctx, d, DeliveryResult{Outcome: outcome, Reason: "summary source has queued messages"}); err != nil {
				t.Fatal(err)
			}
			got, err = h.app.ConversationMaintenance(ctx, "main")
			if err != nil {
				t.Fatal(err)
			}
			want := map[DeliveryOutcome]string{Delivered: "delivered", Failed: "failed", Unknown: "unknown"}[outcome]
			if got.Status != want || got.Operation != "conversation.handoff" || got.RequestID != env.RequestID {
				t.Fatalf("outcome %+v", got)
			}
			if outcome != Delivered && got.Reason != "summary source has queued messages" {
				t.Fatalf("lost failure reason: %+v", got)
			}
		})
	}
}

func TestSummaryAttemptEventsDoNotInvalidateCoverage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := h.app.ClaimSummaryAttempt(ctx, snap, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if h.count("events WHERE kind='conversation.summary_started'") != 1 {
		t.Fatal("duplicate/missing summary start")
	}
	for range 2 {
		if err := h.app.FinishSummaryAttempt(ctx, snap, false); err != nil {
			t.Fatal(err)
		}
	}
	if h.count("events WHERE kind='conversation.summary_failed'") != 1 {
		t.Fatal("duplicate/missing summary failure")
	}
	latest, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil || !snap.Covers(latest) {
		t.Fatalf("maintenance invalidated own coverage %+v %v", latest, err)
	}
}
