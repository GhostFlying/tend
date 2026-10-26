package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func questionRun(t *testing.T, role protocol.RunRole) (*harness, protocol.Assignment, *protocol.RunnerRuntime) {
	t.Helper()
	h := newHarness(t)
	runtime := testRuntime("question-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, task := h.createTask(owner, createTaskPayload{Title: "investigate", Spec: testSpec})
	as, ok := h.claimInputRun(runtime)
	if !ok || !as.WorkerQuestions {
		t.Fatalf("question-capable assignment: %+v", as)
	}
	if role == protocol.RoleAccept {
		h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
		if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		as, ok = h.claimInputRun(runtime)
		if !ok || as.Role != role {
			t.Fatal(as)
		}
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, as, runtime
}

var scopeQuestion = protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Should the loader be changed?", Evidence: "The renderer is correct; the loader accepts two interpretations.", Options: []string{"renderer only", "include loader"}}

func askQuestion(t *testing.T, h *harness, as protocol.Assignment, p protocol.AskOwnerPayload) protocol.WorkerQuestion {
	t.Helper()
	op := h.runAs(as, protocol.OpRunsAskOwner, p, AskOwner{})
	var q protocol.WorkerQuestion
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &q) != nil {
		t.Fatalf("ask: %+v", op)
	}
	return q
}
func answerQuestion(t *testing.T, h *harness, q protocol.WorkerQuestion, option, text string) protocol.Operation {
	t.Helper()
	return h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: q.DecisionID}, protocol.AnswerDecisionPayload{Version: q.Version, Option: option, Text: text}, nil), AnswerDecision{})
}

func TestWorkerQuestionsKeepRunAndExposeProvenance(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			h, as, _ := questionRun(t, role)
			before := h.task(as.TaskID)
			q := askQuestion(t, h, as, scopeQuestion)
			if q.QuestionMessageID == "" || q.Role != role || q.Status != store.DecisionOpen || q.RunID != as.RunID || q.ActionID != as.ActionID || q.SpecVersion != as.SpecVersion {
				t.Fatalf("question: %+v", q)
			}
			if h.task(as.TaskID) != before || h.run(as.RunID).ObservedState != protocol.RunRunning || h.lease(as.RunID).Status != store.LeaseActive {
				t.Fatal("asking changed task/run/lease")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				d, err := store.GetDecision(t.Context(), tx.Q(), q.DecisionID)
				if err != nil {
					return err
				}
				v := views.Decision(d)
				if v.Worker == nil || v.Worker.Evidence != scopeQuestion.Evidence || v.Worker.QuestionMessageID != q.QuestionMessageID {
					t.Fatalf("worker metadata: %+v", v)
				}
				m, err := store.GetMessage(t.Context(), tx.Q(), q.QuestionMessageID)
				if err != nil {
					return err
				}
				if m.Author != "system" || !strings.Contains(m.Body, scopeQuestion.Evidence) {
					t.Fatalf("question notice: %+v", m)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			op := answerQuestion(t, h, q, "", "Please keep the loader unchanged.")
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// An owner response receipt can be lost; repeating it creates no new message.
			replay := answerQuestion(t, h, q, "", "Please keep the loader unchanged.")
			if replay.Status != protocol.OpSucceeded {
				t.Fatal(replay)
			}
			h.app = New(h.store, h.ids, h.clock.Now)
			answered, err := h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), q.DecisionID)
			if err != nil || answered.Status != store.DecisionAnswered || !strings.Contains(string(answered.Answer), "Please keep the loader unchanged.") {
				t.Fatalf("answer after restart: %+v %v", answered, err)
			}
			if h.task(as.TaskID) != before || h.run(as.RunID).ObservedState != protocol.RunRunning {
				t.Fatal("answer ended or retried the original work")
			}
			if h.count("outbox WHERE operation='main_thread.handle_event'") != 1 || h.count("background_sources") != 2 || h.count("outbox WHERE operation='main_thread.handle_message'") != 0 {
				t.Fatal("direct panel answer created an extra owner turn")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				page, err := views.Conversation(t.Context(), tx.Q(), 0, 0, 30)
				if err != nil {
					return err
				}
				owners := 0
				for _, m := range page.Items {
					if m.Author == "owner" {
						owners++
						if m.ReplyTo != q.QuestionMessageID || m.State != protocol.MessageReplied {
							t.Fatalf("owner source: %+v", m)
						}
					}
				}
				if owners != 1 {
					t.Fatalf("owner answer messages = %d", owners)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerQuestionIdentitySurvivesCreateReceiptLoss(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	p := runPrincipal(as.RunID, as.Role)
	env := h.env(protocol.OpRunsAskOwner, runTarget(as.RunID), scopeQuestion, nil)
	first := h.exec(p, env, AskOwner{})
	same := h.exec(p, env, AskOwner{})
	if first.Status != protocol.OpSucceeded || string(first.Result) != string(same.Result) {
		t.Fatal(first, same)
	}
	q := askQuestion(t, h, as, scopeQuestion)
	if h.count("worker_questions") != 1 || h.count("conversation_messages") != 2 {
		t.Fatal("replayed question duplicated facts")
	}
	changed := scopeQuestion
	changed.Question = "Different scope?"
	op := questionOperation(h, runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsAskOwner, runTarget(as.RunID), changed, nil), AskOwner{})
	if op.Error == nil || op.Error.Code != protocol.CodeConflictPayload {
		t.Fatal(op)
	}
	changed = scopeQuestion
	changed.QuestionKey = "other"
	op = questionOperation(h, runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsAskOwner, runTarget(as.RunID), changed, nil), AskOwner{})
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal(op)
	}
	if op := answerQuestion(t, h, q, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	q = askQuestion(t, h, as, scopeQuestion)
	if q.Status != store.DecisionAnswered || !strings.Contains(string(q.Answer), "renderer only") {
		t.Fatalf("repeated key lost answer: %+v", q)
	}
}

func TestFault_WorkerQuestionNoticeFailureRollsBackCreation(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_question_notice BEFORE INSERT ON outbox WHEN NEW.operation='main_thread.handle_event' BEGIN SELECT RAISE(ABORT,'no queue'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpRunsAskOwner, runTarget(as.RunID), scopeQuestion, nil)
	if _, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, AskOwner{}); err == nil {
		t.Fatal("question committed without durable notice")
	}
	if h.count("worker_questions") != 0 || h.count("decisions") != 0 || h.count("conversation_messages") != 0 {
		t.Fatal("failed question leaked state")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER fail_question_notice`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runPrincipal(as.RunID, as.Role), env, AskOwner{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestWorkerQuestionsPauseRetainsAndSupersedeCancels(t *testing.T) {
	for _, change := range []string{"pause", "cancel", "new spec", "ended run"} {
		t.Run(change, func(t *testing.T) {
			h, as, _ := questionRun(t, protocol.RoleImplement)
			q := askQuestion(t, h, as, scopeQuestion)
			var op protocol.Operation
			switch change {
			case "pause":
				op = h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(as.TaskID).ControlRevision)), SetControlMode{})
			case "cancel":
				op = h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), map[string]any{}, rev(h.task(as.TaskID).ControlRevision)), CancelTask{})
			case "new spec":
				spec := testSpec
				spec.Goal = "new scope"
				op = h.exec(owner, h.env("tasks.update_intent", taskTarget(as.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(as.TaskID).ControlRevision)), UpdateIntent{})
			case "ended run":
				op = h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped"}, Release{})
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			got, err := h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), q.DecisionID)
			if err != nil {
				t.Fatal(err)
			}
			if change == "pause" {
				if got.Status != store.DecisionOpen {
					t.Fatal(got)
				}
				if op := answerQuestion(t, h, q, "renderer only", ""); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				got, err = h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), q.DecisionID)
				if err != nil || got.Status != store.DecisionAnswered {
					t.Fatal(got, err)
				}
				return
			}
			if got.Status != store.DecisionCancelled || len(got.Answer) > 0 {
				t.Fatalf("stale question can resume: %+v", got)
			}
			if op := answerQuestion(t, h, q, "renderer only", ""); op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
				t.Fatal(op)
			}
		})
	}
}

func TestWorkerAnswersRequireOwnerSourceButAllowAddon(t *testing.T) {
	for _, mode := range []string{"background", "owner chat", "addon"} {
		t.Run(mode, func(t *testing.T) {
			h, as, _ := questionRun(t, protocol.RoleImplement)
			q := askQuestion(t, h, as, scopeQuestion)
			source := q.QuestionMessageID
			p := mainAgent
			if mode == "owner chat" {
				op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Use renderer only"}, nil), SubmitMessage{})
				var m struct {
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(op.Result, &m); err != nil {
					t.Fatal(err)
				}
				source = m.MessageID
			}
			if mode == "addon" {
				p.Agent = "codex"
				source = ""
			}
			op := questionOperation(h, p, h.env("decisions.resolve_from_message", protocol.Target{Type: "decision", ID: q.DecisionID}, protocol.AnswerDecisionPayload{Version: q.Version, Option: "renderer only", SourceMessageID: source}, nil), AnswerDecision{FromMessage: true})
			if mode == "background" {
				if op.Error == nil || op.Error.Code != protocol.CodeForbidden {
					t.Fatal(op)
				}
			} else if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}

func TestWorkerQuestionCapabilityGuardsRecoveryAndAssignment(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	q := askQuestion(t, h, as, scopeQuestion)
	old := testRuntime("older-runner")
	op := h.versionedHello("linux-1", old)
	if op.Error == nil || !strings.Contains(op.Error.Message, "worker-questions-v1") {
		t.Fatalf("old recovery accepted questions: %+v", op)
	}
	if op := answerQuestion(t, h, q, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old = testRuntime("older-runner")
	op = h.versionedHello("linux-1", old)
	if op.Error == nil {
		t.Fatal("old runner may ignore already answered question")
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		run, err := store.GetRun(t.Context(), tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		assignment, err := buildAssignment(t.Context(), tx.Q(), run, lease, h.app.leaseTTL)
		if err != nil {
			return err
		}
		if !assignment.WorkerQuestions || len(assignment.Questions) != 1 || assignment.Questions[0].Status != store.DecisionAnswered {
			t.Fatalf("recovery assignment lost answers: %+v", assignment)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func questionOperation(h *harness, p protocol.Principal, env *protocol.Envelope, cmd Command) protocol.Operation {
	op, err := h.app.Execute(h.t.Context(), p, env, cmd)
	if err != nil {
		return protocol.Operation{Status: protocol.OpFailed, Error: &protocol.Error{Code: protocol.CodeOf(err), Message: err.Error()}}
	}
	return op
}

func TestWorkerQuestionsUnavailableWithoutNegotiation(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "legacy", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok || as.WorkerQuestions {
		t.Fatalf("legacy assignment changed: %+v", as)
	}
	op := questionOperation(h, runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsAskOwner, runTarget(as.RunID), scopeQuestion, nil), AskOwner{})
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest || h.count("worker_questions") != 0 {
		t.Fatalf("unnegotiated question accepted: %+v", op)
	}
}

func TestAnsweredWorkerQuestionCannotResumeSupersededRun(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	q := askQuestion(t, h, as, scopeQuestion)
	if op := answerQuestion(t, h, q, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.Goal = "different goal"
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(as.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(as.TaskID).ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	got, err := h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), q.DecisionID)
	if err != nil || got.Status != store.DecisionCancelled || len(got.Answer) > 0 {
		t.Fatalf("stale answered run can continue: %+v %v", got, err)
	}
	op := questionOperation(h, runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsAskOwner, runTarget(as.RunID), scopeQuestion, nil), AskOwner{})
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal(op)
	}
}
