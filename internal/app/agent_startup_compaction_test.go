package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func startupCompact(t *testing.T, h *harness, conversation, state string) (string, string, string) {
	t.Helper()
	thread := "original-compact-thread"
	if err := h.app.RecordAgentUsage(t.Context(), conversation, protocol.AgentContextUsage{ThreadID: thread, Revision: 1, Measurement: "observed", CompactSupported: true}); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.AgentConversation(t.Context(), tx.Q(), conversation)
		revision = current.Revision
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env("conversation.compact", protocol.Target{Type: "conversation", ID: conversation}, struct{}{}, rev(revision))
	result := h.exec(owner, env, CompactConversation{})
	if result.Status != protocol.OpAccepted {
		t.Fatal(result)
	}
	var compact struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(result.Result, &compact); err != nil || compact.ID == "" {
		t.Fatal("compact fixture lost its operation identity", compact, err)
	}
	var request string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM outbox WHERE operation='conversation.compact' AND json_extract(payload_json,'$.operation_id')=?`, compact.ID).Scan(&request)
	}); err != nil {
		t.Fatal(err)
	}
	if state == "queued" {
		return request, compact.ID, thread
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok || delivery.RequestID != request {
		t.Fatal("compact fixture did not claim its original request", delivery, ok, err)
	}
	if err := h.app.SetCompactState(t.Context(), compact.ID, thread, "starting"); err != nil {
		t.Fatal(err)
	}
	outcome := Unknown
	if state == "completed" {
		if err := h.app.CompleteNativeCompact(t.Context(), conversation, thread, "original-compact-item"); err != nil {
			t.Fatal(err)
		}
		outcome = Delivered
	} else if state != "starting" {
		if err := h.app.SetCompactState(t.Context(), compact.ID, thread, state); err != nil {
			t.Fatal(err)
		}
		if state == "failed" {
			outcome = Failed
		}
	}
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: outcome}); err != nil {
		t.Fatal(err)
	}
	return request, compact.ID, thread
}

func holdStartupInput(t *testing.T, h *harness, conversation, request string) protocol.Attention {
	t.Helper()
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := h.app.DeferAgentStartupAttachment(t.Context(), conversation, AgentStartupAttachment{RequestID: request}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	return startupAttention(t, h)
}

func TestAgentStartupUncertainCompactRetainsOriginalAttachment(t *testing.T) {
	for _, state := range []string{"starting", "running", "unknown"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			request, compact, thread := startupCompact(t, h, "main", state)
			input := startupInput(t, h, "main")
			if input.RequestID == request {
				t.Fatal("fixture did not retain the newer ordinary input")
			}
			attention := holdStartupInput(t, h, "main", input.RequestID)
			_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
			if err != nil || !attachment.Reconciliation || attachment.RequestID != request {
				t.Fatal("held input hid the original uncertain compact attachment", attachment, err)
			}
			if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || !allowed {
				t.Fatal("held input suppressed original native compact discovery", allowed, err)
			}
			if d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
				t.Fatal("compact discovery admitted the held ordinary input", d, ok, err)
			}
			if err := h.app.CompleteNativeCompact(t.Context(), "main", thread, "original-compact-item"); err != nil {
				t.Fatal(err)
			}
			if current, err := h.app.CompactState(t.Context(), compact); err != nil || current != "completed" {
				t.Fatal("original compact did not settle", current, err)
			}
			if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || allowed {
				t.Fatal("settled compact exempted the newer input from its hold", allowed, err)
			}
			if current := readAttentionForTest(t, h, attention.AttentionID); current.Status != "open" {
				t.Fatal("compact completion silently resolved the newer input action", current)
			}
			if h.count("agent_compactions") != 1 || h.count("main_turns") != 0 {
				t.Fatal("compact discovery replayed work or fabricated a native turn")
			}
		})
	}
}

func TestAgentStartupCompactExemptionStaysWithinItsNativeBoundary(t *testing.T) {
	for _, state := range []string{"queued", "completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			startupCompact(t, h, "main", state)
			env := h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Keep the newer ordinary input on its own hold"}, nil)
			if result := h.exec(owner, env, SubmitMessage{}); result.Status != protocol.OpAccepted {
				t.Fatal(result)
			}
			var request string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM outbox WHERE parent_request_id=? AND destination=?`, env.RequestID, MainThreadDestination).Scan(&request)
			}); err != nil {
				t.Fatal(err)
			}
			holdStartupInput(t, h, "main", request)
			_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
			if err != nil || attachment.Reconciliation {
				t.Fatal("queued or ended compact received native uncertainty exemption", attachment, err)
			}
		})
	}
	t.Run("other_conversation", func(t *testing.T) {
		h := newHarness(t)
		work := h.createWork(workPlan())
		h.prepareAgent(work.WorkItemID)
		h.exec(owner, h.env("conversation.open_background", workTarget(work.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
		startupCompact(t, h, "work-item:"+work.WorkItemID, "unknown")
		input := startupInput(t, h, "main")
		holdStartupInput(t, h, "main", input.RequestID)
		if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || allowed {
			t.Fatal("another conversation's compact bypassed this input's hold", allowed, err)
		}
	})
}
