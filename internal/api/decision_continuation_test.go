package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDecisionContinuationOwnerRoutesAndOriginalMissingReceipts(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	route := "/api/v1/decisions/missing/continuation"
	if status := e.do(http.MethodGet, route, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	if status := e.do(http.MethodGet, route, token, nil, nil); status != http.StatusNotFound {
		t.Fatal(status)
	}
	zero := int64(0)
	for _, operation := range []string{protocol.OpResumeDecisionContinuation, protocol.OpReconcileDecisionContinuation} {
		env := e.envelope(operation, protocol.Target{Type: "decision", ID: "missing"}, &zero, protocol.ResumeDecisionContinuationPayload{ExpectedRevision: 0})
		status, first := e.op(token, env)
		if status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpFailed || first.Operation.Error == nil || first.Operation.Error.Code != protocol.CodeNotFound {
			t.Fatal(operation, status, first)
		}
		status, replay := e.op(token, env)
		if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
			t.Fatal("original missing continuation receipt changed", status, replay)
		}
	}
}

func TestDecisionContinuationOwnerReadAndTwoRevisionCAS(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	status, created := e.op(token, e.envelope("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision,
		protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Question continuation", Rating: protocol.RatingP1,
			Spec: protocol.OwnerTaskSpec{Goal: "Preserve a bounded handoff", Acceptance: []string{"Keep the original identity"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}}))
	if status != http.StatusOK || created.Operation == nil || created.Operation.Error != nil {
		t.Fatalf("HTTP%d operation=%+v", status, created.Operation)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Operation.Result, &task); err != nil || task.TaskID == "" {
		t.Fatal(created, err)
	}
	decision, run := e.ids.New(protocol.PrefixDecision), e.ids.New(protocol.PrefixRun)
	policy := protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Await an independent owner resume"}
	// Seed bounded historical handoff facts. This test exercises owner routing,
	// projection and CAS, not worker execution or physical stop evidence.
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		actions, err := store.ListActions(t.Context(), tx.Q(), task.TaskID)
		if err != nil {
			return err
		}
		if len(actions) == 0 {
			return fmt.Errorf("missing task action")
		}
		if err := tx.InsertRun(t.Context(), store.RunRow{RunID: run, TaskID: task.TaskID, ActionID: actions[0].ActionID, Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredRun, ObservedState: protocol.RunRunning}); err != nil {
			return err
		}
		if err := tx.InsertDecision(t.Context(), decision, task.TaskID, "Continue after the independent decision?", `["acknowledged"]`, `{}`, nil, 1); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE decisions SET status='answered',answer_json='{"text":"Wait"}' WHERE decision_id=?`, decision); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO run_question_handoffs(run_id,decision_id,stopped) VALUES(?,?,0)`, run, decision); err != nil {
			return err
		}
		return tx.PutDecisionContinuation(t.Context(), store.DecisionContinuationRow{DecisionID: decision, Revision: 3, Policy: policy, State: "waiting", AnswerRequestID: "original-answer", CreatedAt: 1, UpdatedAt: 1})
	}); err != nil {
		t.Fatal(err)
	}
	before := e.count(`SELECT COUNT(*) FROM request_records`)
	var view protocol.DecisionContinuationView
	if status := e.do(http.MethodGet, "/api/v1/decisions/"+decision+"/continuation", token, nil, &view); status != http.StatusOK || view.DecisionID != decision || view.RunID != run || view.TaskID != task.TaskID || view.Revision != 3 || view.State != "waiting" || view.BlockedReason != "explicit_resume_required" || view.ResumeCondition != "explicit_resume" || !reflect.DeepEqual(view.Policy, &policy) || view.AnswerRequestID != "original-answer" {
		t.Fatal(status, view)
	}
	if got := e.count(`SELECT COUNT(*) FROM request_records`); got != before {
		t.Fatal("continuation read wrote a business request", before, got)
	}
	for _, operation := range []string{protocol.OpResumeDecisionContinuation, protocol.OpReconcileDecisionContinuation} {
		for _, staleTask := range []bool{false, true} {
			revision, continuationRevision := view.TaskControlRevision, view.Revision
			if staleTask {
				revision++
			} else {
				continuationRevision++
			}
			env := e.envelope(operation, protocol.Target{Type: "decision", ID: decision}, &revision, protocol.ResumeDecisionContinuationPayload{ExpectedRevision: continuationRevision})
			status, first := e.op(token, env)
			if status != http.StatusOK || first.Operation == nil || first.Operation.Error == nil || first.Operation.Error.Code != protocol.CodeStaleRevision {
				t.Fatal(operation, staleTask, status, first)
			}
			status, replay := e.op(token, env)
			if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
				t.Fatal("CAS receipt changed on original request replay", first, replay)
			}
		}
	}
	if e.count(`SELECT COUNT(*) FROM run_question_handoffs WHERE continuation_action_id IS NOT NULL`) != 0 {
		t.Fatal("stale owner control manufactured a continuation")
	}
	// A real owner mutation admits the independently versioned historical wait;
	// the answer identity remains untouched and lost responses replay exactly.
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='finished' WHERE run_id=?`, run); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE run_question_handoffs SET stopped=1 WHERE run_id=?`, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	envelope := e.envelope(protocol.OpResumeDecisionContinuation, protocol.Target{Type: "decision", ID: decision}, &view.TaskControlRevision, protocol.ResumeDecisionContinuationPayload{ExpectedRevision: view.Revision})
	status, first := e.op(token, envelope)
	if status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("HTTP%d operation=%+v", status, first.Operation)
	}
	status, replay := e.op(token, envelope)
	if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
		t.Fatal("original owner resume receipt changed", first, replay)
	}
	var admitted protocol.DecisionContinuationView
	if err := json.Unmarshal(first.Operation.Result, &admitted); err != nil || admitted.State != "continued" || admitted.ContinuationActionID == "" || admitted.AnswerRequestID != "original-answer" || admitted.ResumeRequestID != envelope.RequestID {
		t.Fatal(admitted, err)
	}
	if e.count(`SELECT COUNT(*) FROM run_question_handoffs WHERE continuation_action_id IS NOT NULL`) != 1 {
		t.Fatal("resume failed to commit exactly one successor")
	}
}

func TestDecisionContinuationTypedAnswerRouteRequiresCompletePolicy(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	revision := int64(0)
	for _, typed := range []bool{false, true} {
		payload := protocol.AnswerDecisionPayload{Version: 1, Text: "Wait"}
		want := protocol.CodeInvalidRequest
		if typed {
			payload.Continuation = &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Await original dependency"}
			want = protocol.CodeNotFound
		}
		env := e.envelope(protocol.OpAnswerDecisionContinuation, protocol.Target{Type: "decision", ID: "missing"}, &revision, payload)
		status, first := e.op(token, env)
		if status != http.StatusOK || first.Operation == nil || first.Operation.Error == nil || first.Operation.Error.Code != want {
			t.Fatalf("typed=%v HTTP%d %+v", typed, status, first.Operation)
		}
		status, replay := e.op(token, env)
		if status != http.StatusOK || !reflect.DeepEqual(first.Operation, replay.Operation) {
			t.Fatal("typed answer lost original receipt", first, replay)
		}
	}
}
