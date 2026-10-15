package e2e

import (
	"context"
	"encoding/json"
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
func (s *server) startMainThread(h *testkit.Harness) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	s.t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	m := mainthread.New(s.app, contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{}), tools.New(s.app, s.store, quiet()), mainthread.Config{
		Instance: "main", Thread: harness.ThreadConfig{Cwd: "/agent", DeveloperInstructions: mainthread.Instructions},
		Open: h.Open, RestartDelay: 20 * time.Millisecond, Log: quiet(),
	})
	d := dispatch.New(s.app, map[string]dispatch.Handler{app.MainThreadDestination: m}, s.signal, time.Now, dispatch.Config{PollInterval: 50 * time.Millisecond}, quiet())
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
		`SELECT COUNT(*) FROM tasks`:                                                                         1,
		`SELECT COUNT(*) FROM runs WHERE role = 'implement'`:                                                 2,
		`SELECT COUNT(*) FROM runs WHERE role = 'accept'`:                                                    2,
		`SELECT COUNT(*) FROM artifacts`:                                                                     2,
		`SELECT COUNT(*) FROM acceptances`:                                                                   2,
		`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`:                                         1,
		`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent'`:                             1,
		`SELECT COUNT(*) FROM (SELECT effect_key FROM plan_actions GROUP BY effect_key HAVING COUNT(*) > 1)`: 0,
		`SELECT COUNT(*) FROM (SELECT event_id FROM events GROUP BY event_id HAVING COUNT(*) > 1)`:           0,
	} {
		if got := srv.count(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}
