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
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

// startMainThread runs the main thread manager and the dispatcher with a
// fake main agent.
func (s *server) startMainThread(h *testkit.Harness, background ...func(*testkit.FakeThread, string, harness.TurnInput) string) {
	originalTurn := h.Turn
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.HasPrefix(th.Config.Cwd, "/background/") && len(background) > 0 {
			return background[0](th, turn, in)
		}
		if in.Repair {
			return completeFixtureReview(s.t, th, turn)
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

// TestEndToEnd_FakeHarness drives the whole loop: a message to the main
// agent creates a task, a runner implements it, acceptance asks for
// changes, the second generation passes and the task completes.
func TestEndToEnd_FakeHarness(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		res := th.CallTool(context.Background(), turn, "call_1", "tasks_create", map[string]any{
			"title": "fix the build", "commit_seq": 1, "workspace_name": "fix-build",
			"spec": domain.Spec{Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: domain.DeliveryCommit,
				Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}},
		})
		th.Say(turn, "item_1", "On it: "+res.Text)
		return "completed"
	}
	srv.startMainThread(agent)

	var mu sync.Mutex
	verdicts := []protocol.Verdict{protocol.VerdictChangesRequired, protocol.VerdictPassed}
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role != protocol.RoleAccept {
			return passing(spec)
		}
		mu.Lock()
		v := verdicts[0]
		verdicts = verdicts[1:]
		mu.Unlock()
		return testkit.WorkerScript{Result: harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: v, Evidence: json.RawMessage(`{}`)}}}
	})
	n.start()

	srv.say("The build is broken, please fix it.")
	eventually(t, "the task completes", func() bool {
		return srv.count(`SELECT COUNT(*) FROM tasks WHERE lifecycle = 'completed'`) == 1
	})
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM tasks`:                         1,
		`SELECT COUNT(*) FROM runs WHERE role = 'implement'`: 2,
		`SELECT COUNT(*) FROM runs WHERE role = 'accept'`:    2,
		`SELECT COUNT(*) FROM artifacts`:                     2,
		`SELECT COUNT(*) FROM acceptances`:                   2,
		`SELECT COUNT(*) FROM main_turns WHERE status = 'completed' AND request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1)`: 1,
		`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent'`:                                                                           1,
		`SELECT COUNT(*) FROM (SELECT effect_key FROM plan_actions GROUP BY effect_key HAVING COUNT(*) > 1)`:                                               0,
		`SELECT COUNT(*) FROM (SELECT event_id FROM events GROUP BY event_id HAVING COUNT(*) > 1)`:                                                         0,
	} {
		if got := srv.count(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}

func completeFixtureReview(t *testing.T, th *testkit.FakeThread, turn string) string {
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
	for _, c := range state.Contexts {
		if !invoke("work_item_context_prepare", map[string]any{"work_item_id": c.WorkItemID, "expected_control_revision": c.Revision, "coverage": "Reviewed transport fixture messages; no additional semantic facts", "gaps": "None in this fixture"}, nil) {
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
