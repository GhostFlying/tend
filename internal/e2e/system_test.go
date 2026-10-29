package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

// startMainThread runs the main thread manager and the dispatcher with a
// fake main agent.
func (s *server) startHistoricalMainThread(h *testkit.Harness, background ...func(*testkit.FakeThread, string, harness.TurnInput) string) {
	originalTurn := h.Turn
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.HasPrefix(th.Config.Cwd, "/background/") && len(background) > 0 {
			return background[0](th, turn, in)
		}
		if in.Repair {
			return completeHistoricalFixtureReview(s, th, turn)
		}
		if originalTurn != nil {
			return originalTurn(th, turn, in)
		}
		return "completed"
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	s.onStop(func() {
		cancel()
		wg.Wait()
	})
	cb := contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{})
	m := mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, quiet()), mainthread.Config{
		Instance: "main", Thread: harness.ThreadConfig{Cwd: "/agent", DeveloperInstructions: mainthread.Instructions},
		Open: h.Open, RestartDelay: 20 * time.Millisecond, Log: quiet(),
	})
	handlers := map[string]dispatch.Handler{app.MainThreadDestination: m, app.MainThreadControlDestination: m.Control()}
	if len(background) > 0 {
		pool := &mainthread.BackgroundPool{App: s.app, Front: m, New: func(id string) (*mainthread.Manager, error) {
			return mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, quiet()), mainthread.Config{Instance: "main/" + id, ConversationID: id, Thread: harness.ThreadConfig{Cwd: "/background/" + id}, Open: h.Open, RestartDelay: 20 * time.Millisecond, Log: quiet()}), nil
		}}
		handlers[app.MainThreadControlDestination] = pool
		wg.Go(func() { pool.Run(ctx) })
	}
	d := dispatch.New(s.app, handlers, s.signal, time.Now, dispatch.Config{PollInterval: 50 * time.Millisecond}, quiet())
	wg.Go(func() { _ = m.Run(ctx) })
	wg.Go(func() { _ = d.Run(ctx) })
}

func (s *server) say(body string) {
	s.t.Helper()
	s.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, map[string]string{"body": body}, app.SubmitMessage{})
}

func completeHistoricalFixtureReview(s *server, th *testkit.FakeThread, turn string) string {
	t := s.t
	t.Helper()
	call := 0
	invoke := func(name string, args any, out any) bool {
		call++
		result := th.CallTool(context.Background(), turn, fmt.Sprintf("review-%d", call), name, args)
		var answer struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
		}
		if !result.Success || json.Unmarshal([]byte(result.Text), &answer) != nil || !answer.OK {
			return false
		}
		return out == nil || json.Unmarshal(answer.Result, out) == nil
	}
	var state protocol.LogicalTurnState
	if !invoke("conversation_turn_read", struct{}{}, &state) {
		return "failed"
	}
	for _, m := range state.Messages {
		if m.Disposition != "pending" {
			continue
		}
		ids := []string{}
		for _, i := range m.Items {
			ids = append(ids, i.WorkItemID)
		}
		disposition := "unassigned"
		if len(ids) > 0 {
			disposition = "linked"
		}
		if !invoke("conversation_associate", map[string]any{"message_id": m.MessageID, "content_revision": m.ContentRevision, "expected_control_revision": m.Revision, "disposition": disposition, "work_item_ids": ids}, nil) {
			return "failed"
		}
	}
	if !invoke("conversation_turn_read", struct{}{}, &state) {
		return "failed"
	}
	// Explicit old-ledger setup for historical transport tests, never a
	// runtime alias or fake successful call to the retired planning tool.
	for _, c := range state.Contexts {
		delivery, found, err := s.app.TurnDelivery(t.Context(), th.ID(), turn)
		if err != nil || !found {
			t.Errorf("historical delivery: %v %v", found, err)
			return "failed"
		}
		payload, _ := json.Marshal(protocol.PrepareWorkItemContextPayload{Coverage: "Reviewed transport fixture messages; no additional semantic facts", Gaps: "None in this fixture"})
		op, err := s.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "historical-fixture", Thread: th.ID()}, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), ParentRequestID: delivery.RequestID, Operation: "work_item_context.prepare", Target: protocol.Target{Type: "work_item", ID: c.WorkItemID}, ExpectedControlRevision: &c.Revision, Payload: payload}, app.WithExecutionRequest(app.ContextCommand{Action: "prepare"}, delivery.RequestID))
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Errorf("historical context setup: %+v %v", op, err)
			return "failed"
		}
	}

	if !invoke("conversation_turn_read", struct{}{}, &state) {
		return "failed"
	}
	review := protocol.LogicalTurnCommit{Contexts: state.Contexts}
	for _, m := range state.Messages {
		review.Messages = append(review.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
	}
	if !invoke("conversation_turn_commit", review, nil) {
		return "failed"
	}
	return "completed"
}

func businessStarts(h *testkit.Harness) []harness.TurnInput {
	var out []harness.TurnInput
	for _, v := range h.Starts() {
		if !v.Repair {
			out = append(out, v)
		}
	}
	return out
}

// Legacy assessments bind to the immutable context actually delivered at turn
// admission, rather than a later read of current work-item metadata.
func historicalDeliveredContextRevision(t *testing.T, h *testkit.Harness, thread string) int64 {
	t.Helper()
	var delivered int64
	for _, text := range h.Injected(thread) {
		var revision int64
		if _, err := fmt.Sscanf(text, "Work-item context at revision %d.", &revision); err == nil {
			delivered = revision
		}
	}
	if delivered == 0 {
		t.Error("missing injected historical context revision")
	}
	return delivered
}
