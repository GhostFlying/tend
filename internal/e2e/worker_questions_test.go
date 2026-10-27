package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEndToEnd_WorkerQuestionsKeepRunAndLease(t *testing.T) {
	clock := testkit.NewClock()
	const ttl = time.Minute
	srv := newServer(t, ttl, clock.Now)
	srv.startMainThread(testkit.NewHarness())
	web := srv.webLogin(srv.startWeb().URL)
	answers := make(chan protocol.WorkerQuestion, 2)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		script.Act = func(ctx context.Context) error {
			if spec.AskOwner == nil {
				return fmt.Errorf("worker question capability missing")
			}
			q, err := spec.AskOwner(ctx, protocol.AskOwnerPayload{
				QuestionKey: "scope", Question: "Which scope should I verify?",
				Evidence: "The two paths have different compatibility costs.", Options: []string{"narrow", "broad"},
			})
			if err == nil {
				answers <- q
			}
			return err
		}
		return script
	})
	n.heartbeat = 20 * time.Millisecond
	n.log = runnerTestLog(t)
	n.start()
	task := srv.createTask()
	n.diagnoseFailure(task)
	j := n.journal()
	defer j.Close()
	for i, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		var decision protocol.DecisionView
		eventually(t, "worker question becomes visible to owner", func() bool {
			for _, d := range web.decisions() {
				if d.Worker != nil && d.Worker.Role == role {
					decision = d
					return true
				}
			}
			return false
		})
		if decision.TaskID != task || decision.Worker.Evidence == "" || decision.Worker.QuestionMessageID == "" {
			t.Fatalf("question lost provenance: %+v", decision)
		}
		initial := workerQuestionLease(t, srv, decision.Worker.RunID)
		original, err := j.Run(t.Context(), initial.RunID)
		if err != nil || original.Thread == "" {
			t.Fatalf("waiting worker thread: %q, %v", original.Thread, err)
		}
		// Advance only after observing the previous renewal. The minute TTL
		// bounds real HTTP requests generously; fake time still crosses the
		// original deadline while the question remains unanswered.
		for clock.Now().UnixMilli() <= initial.ExpiresAt {
			clock.Advance(ttl / 4)
			expires := clock.Now().Add(ttl).UnixMilli()
			eventually(t, "waiting worker renews the original lease", func() bool {
				lease := workerQuestionLease(t, srv, initial.RunID)
				return lease.LeaseID == initial.LeaseID && lease.Status == store.LeaseActive && lease.ExpiresAt >= expires
			})
		}
		if got := len(n.workers.Started()); got != i+1 {
			t.Fatalf("question caused extra worker starts: %d, want %d (role=%s run=%s)", got, i+1, role, initial.RunID)
		}
		current, err := j.Run(t.Context(), initial.RunID)
		if err != nil || current.Thread != original.Thread || current.Attempts != 1 || current.Fenced {
			t.Fatalf("waiting worker changed: thread=%s attempts=%d fenced=%t err=%v", current.Thread, current.Attempts, current.Fenced, err)
		}
		if got := srv.count(`SELECT COUNT(*) FROM worker_questions w JOIN decisions d ON d.decision_id=w.decision_id WHERE w.run_id=? AND d.status='open'`, initial.RunID); got != 1 {
			t.Fatalf("waiting questions = %d, want 1", got)
		}
		payload := protocol.AnswerDecisionPayload{Version: decision.Version}
		if role == protocol.RoleImplement {
			payload.Option = "narrow"
		} else {
			payload.Text = "Verify both paths and report any uncertainty."
		}
		web.op(srv.ids.New(protocol.PrefixRequest), "decisions.respond", protocol.Target{Type: "decision", ID: decision.DecisionID}, payload)
		select {
		case answer := <-answers:
			var recorded struct{ Option, Text string }
			if err := json.Unmarshal(answer.Answer, &recorded); err != nil || recorded.Option != payload.Option || recorded.Text != payload.Text || answer.RunID != decision.Worker.RunID {
				t.Fatalf("worker received wrong answer: %+v, %v", answer, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not receive owner answer")
		}
		if got := srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE author='owner' AND reply_to=?`, decision.Worker.QuestionMessageID); got != 1 {
			t.Fatalf("direct answer reply association count = %d", got)
		}
	}
	eventually(t, "both workers finish the original task", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=?`, task); got != 2 {
		t.Fatalf("question introduced replacement runs: %d", got)
	}
}

func workerQuestionLease(t *testing.T, srv *server, runID string) store.LeaseRow {
	t.Helper()
	var lease store.LeaseRow
	if err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		lease, err = store.GetLeaseByRun(t.Context(), tx.Q(), runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestFault_WorkerQuestionSurvivesHeartbeatLoss(t *testing.T) {
	clock := testkit.NewClock()
	const ttl = time.Minute
	srv := newServer(t, ttl, clock.Now)
	srv.startMainThread(testkit.NewHarness())
	web := srv.webLogin(srv.startWeb().URL)
	answers := make(chan protocol.WorkerQuestion, 1)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		if spec.Role == protocol.RoleImplement {
			script.Act = func(ctx context.Context) error {
				q, err := spec.AskOwner(ctx, protocol.AskOwnerPayload{
					QuestionKey: "scope", Question: "Keep the existing constraint?", Evidence: "The constraint applies to both paths.",
				})
				if err == nil {
					answers <- q
				}
				return err
			}
		}
		return script
	})
	n.heartbeat = 20 * time.Millisecond
	n.log = runnerTestLog(t)
	n.start()
	task := srv.createTask()
	n.diagnoseFailure(task)
	var decision protocol.DecisionView
	eventually(t, "worker question is waiting", func() bool {
		for _, d := range web.decisions() {
			if d.TaskID == task && d.Worker != nil {
				decision = d
				return true
			}
		}
		return false
	})
	first := n.workers.Started()[0]
	j := n.journal()
	defer j.Close()
	original, err := j.Run(t.Context(), first.RunID)
	if err != nil || original.Thread == "" {
		t.Fatalf("original waiting thread: %q, %v", original.Thread, err)
	}
	n.net.disconnect(t)
	clock.Advance(ttl + time.Millisecond)
	eventually(t, "waiting worker stops after its lease is lost", func() bool {
		return len(n.workers.Stopped()) == 1
	})
	eventually(t, "waiting run is durably fenced and unknown", func() bool {
		run, err := j.Run(t.Context(), first.RunID)
		return err == nil && run.Fenced && srv.runState(protocol.RoleImplement) == protocol.RunUnknown
	})
	if got := len(n.workers.Started()); got != 1 {
		t.Fatalf("worker restarted before reconciliation: %d starts", got)
	}
	n.net.set(false)
	eventually(t, "waiting worker resumes after reconciliation", func() bool { return len(n.workers.Started()) == 2 })
	resumed := n.workers.Started()[1]
	if resumed.RunID != first.RunID || resumed.Thread != original.Thread || resumed.Dir != first.Dir || resumed.Attempt != 2 {
		t.Fatalf("resume changed identity: run=%s thread=%s attempt=%d same_workspace=%t", resumed.RunID, resumed.Thread, resumed.Attempt, resumed.Dir == first.Dir)
	}
	if len(resumed.Questions) != 1 || resumed.Questions[0].DecisionID != decision.DecisionID || resumed.Questions[0].QuestionMessageID != decision.Worker.QuestionMessageID {
		t.Fatal("resume lost the original waiting question")
	}
	web.op(srv.ids.New(protocol.PrefixRequest), "decisions.respond", protocol.Target{Type: "decision", ID: decision.DecisionID},
		protocol.AnswerDecisionPayload{Version: decision.Version, Text: "Keep the existing constraint."})
	select {
	case answer := <-answers:
		if answer.DecisionID != decision.DecisionID || answer.RunID != first.RunID || answer.Status != "answered" {
			t.Fatalf("resumed worker received another answer: run=%s decision=%s status=%s", answer.RunID, answer.DecisionID, answer.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resumed worker did not receive the answer")
	}
	eventually(t, "recovered task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=?`, task); got != 2 {
		t.Fatalf("recovery introduced replacement runs: %d", got)
	}
	if got := srv.count(`SELECT COUNT(*) FROM worker_questions WHERE run_id=?`, first.RunID); got != 1 {
		t.Fatalf("recovery duplicated the question: %d", got)
	}
	if got := srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE author='owner' AND reply_to=?`, decision.Worker.QuestionMessageID); got != 1 {
		t.Fatalf("recovered answer reply association count = %d", got)
	}
	if got := len(n.workers.Started()); got != 3 {
		t.Fatalf("recovered task worker starts = %d, want implement, resume, accept", got)
	}
}

func TestEndToEnd_MainRelaysOwnerAnswerToWaitingWorker(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	toolResults := make(chan harness.ToolResult, 1)
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if choice := chosen.FindStringSubmatch(in.Text); choice != nil {
			version, _ := strconv.Atoi(choice[3])
			toolResults <- th.CallTool(t.Context(), turn, "answer_"+turn, "decisions_resolve", map[string]any{
				"decision_id": choice[2], "version": version, "option": choice[1],
			})
		}
		return "completed"
	}
	srv.startMainThread(agent)
	web := srv.webLogin(srv.startWeb().URL)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		if spec.Role == protocol.RoleImplement {
			script.Act = func(ctx context.Context) error {
				if spec.AskOwner == nil {
					return fmt.Errorf("worker question capability missing")
				}
				_, err := spec.AskOwner(ctx, protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Keep the current behavior?",
					Evidence: "The existing implementation preserves archived entries.", Options: []string{"keep", "change"}})
				return err
			}
		}
		return script
	})
	n.start()
	task := srv.createTask()
	var question protocol.DecisionView
	eventually(t, "worker question reaches chat", func() bool {
		for _, d := range web.decisions() {
			if d.Worker != nil {
				question = d
				return true
			}
		}
		return false
	})
	message := web.send(protocol.SubmitMessagePayload{Body: "Keep the current behavior.", ReplyTo: question.Worker.QuestionMessageID,
		Decision: &protocol.DecisionChoice{DecisionID: question.DecisionID, Version: question.Version, Option: "keep"}})
	select {
	case result := <-toolResults:
		if !result.Success {
			t.Fatalf("main could not record actual owner answer: %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("main did not relay owner answer")
	}
	eventually(t, "original worker completes after chat answer", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM decisions WHERE decision_id=? AND answered_message_id=?`, question.DecisionID, message); got != 1 {
		t.Fatal("worker answer lost its actual owner source message")
	}
	if got := srv.count(`SELECT COUNT(*) FROM worker_questions`); got != 1 {
		t.Fatalf("main duplicated worker question: %d", got)
	}
}

func TestFault_WorkerQuestionSurvivesRunnerRestartAndLostReceipt(t *testing.T) {
	srv := newServer(t, time.Minute)
	srv.startMainThread(testkit.NewHarness())
	web := srv.webLogin(srv.startWeb().URL)
	answers := make(chan protocol.WorkerQuestion, 1)
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		if spec.Role != protocol.RoleImplement {
			return script
		}
		script.Act = func(ctx context.Context) error {
			if spec.AskOwner == nil {
				return fmt.Errorf("worker question capability missing")
			}
			if spec.Attempt > 1 && (len(spec.Questions) != 1 || spec.Questions[0].QuestionKey != "scope") {
				return fmt.Errorf("resume lost original question: %+v", spec.Questions)
			}
			q, err := spec.AskOwner(ctx, protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Use the existing constraint?", Evidence: "The original constraint applies to both paths."})
			if err == nil {
				answers <- q
			}
			return err
		}
		return script
	})
	n.net.lose = map[string]int{protocol.OpRunsAskOwner: 1}
	n.start()
	task := srv.createTask()
	var decision protocol.DecisionView
	eventually(t, "question survives its lost creation receipt", func() bool {
		for _, d := range web.decisions() {
			if d.Worker != nil {
				decision = d
				return true
			}
		}
		return false
	})
	eventually(t, "first worker has its thread", func() bool { return len(n.workers.Started()) == 1 })
	first := n.workers.Started()[0]
	n.stop()
	n.start()
	eventually(t, "same worker resumes question", func() bool { return len(n.workers.Started()) == 2 })
	resumed := n.workers.Started()[1]
	if resumed.RunID != first.RunID || resumed.Thread != fmt.Sprintf("th_%s_1", first.RunID) || resumed.Dir != first.Dir {
		t.Fatalf("resume changed run/thread/workspace: first=%+v resumed=%+v", first, resumed)
	}
	web.op(srv.ids.New(protocol.PrefixRequest), "decisions.respond", protocol.Target{Type: "decision", ID: decision.DecisionID},
		protocol.AnswerDecisionPayload{Version: decision.Version, Text: "Keep the existing constraint."})
	select {
	case answer := <-answers:
		if answer.DecisionID != decision.DecisionID {
			t.Fatalf("resume received another question: %+v", answer)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resumed worker did not receive answer")
	}
	eventually(t, "resumed task completes", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	if got := srv.count(`SELECT COUNT(*) FROM worker_questions WHERE run_id=?`, first.RunID); got != 1 {
		t.Fatalf("recovery duplicated question: %d", got)
	}
}
