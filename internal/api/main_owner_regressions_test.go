package api

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
	"strings"
	"testing"
)

func TestMainOwnerRegressionMainAttentionDoesNotWakeItself(t *testing.T) {
	e, call := mainOwnerFixture(t)
	before := e.count(`SELECT COUNT(*) FROM outbox WHERE destination='main_thread'`)
	payload, _ := json.Marshal(protocol.CreateAttentionPayload{Kind: "information", Title: "Already recorded in current turn"})
	response, err := ownerToolInvoke(t, e, call, "attentions.create", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "attention"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	if after := e.count(`SELECT COUNT(*) FROM outbox WHERE destination='main_thread'`); after != before {
		t.Fatalf("own information created %d additional main wake(s)", after-before)
	}
}

func TestMainOwnerRegressionNamedTypedAnswerUsesOwnerAuthority(t *testing.T) {
	e, call := mainOwnerFixture(t)
	task := e.task("Worker needing a decision")
	rt := &protocol.RunnerRuntime{InstanceID: "question-runner", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs, protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance}}
	runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}
	issueWorkerCommand(t, e, runner, protocol.Target{Type: "host", ID: "host-a"}, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: rt}, app.Hello{})
	claimed := issueWorkerCommand(t, e, runner, protocol.Target{Type: "host", ID: "host-a"}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: rt}, app.Claim{})
	var result struct {
		Assignment protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claimed.Result, &result); err != nil || result.Assignment.TaskID != task {
		t.Fatal(result, err)
	}
	as := result.Assignment
	worker := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	target := protocol.Target{Type: "run", ID: as.RunID}
	issueWorkerCommand(t, e, worker, target, nil, app.ReportRunStarted{})
	asked := issueWorkerCommand(t, e, worker, target, protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Continue?", Options: []string{"continue"}}, app.AskOwner{})
	var question protocol.WorkerQuestion
	if err := json.Unmarshal(asked.Result, &question); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetTask(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		revision = row.ControlRevision
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turns(logical_turn_id,message_id,status,updated_at) SELECT request_id,message_id,'processing',updated_at FROM main_turns WHERE request_id=?`, call.ExecutionID); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO logical_turn_executions(request_id,logical_turn_id) VALUES(?,?)`, call.ExecutionID, call.ExecutionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(protocol.AnswerDecisionPayload{Version: question.Version, Option: "continue", Continuation: &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Wait for independent work"}})
	args, _ := json.Marshal(map[string]any{"target_id": question.DecisionID, "expected_control_revision": revision, "effect_seq": 1, "payload": json.RawMessage(payload)})
	service := tools.New(e.app, e.store, e.context, quiet())
	service.SetOwnerTools(e.server.MainOwnerTools())
	named := service.Handle(t.Context(), tools.Call{Principal: call.Actor, ExecutionRequestID: call.ExecutionID, TurnID: "turn", Native: "typed-answer", Tool: "decision_continuation_answer", Arguments: args})
	if !named.Success {
		t.Errorf("named owner answer failed: %s", named.Text)
	}

}

func TestMainOwnerRegressionLargeValidMutationReturnsItsReceipt(t *testing.T) {
	e, call := mainOwnerFixture(t)
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Large supported definition", Project: "default", Intent: strings.Repeat("x", (1<<20)-300), Stages: []protocol.IssueStage{}}})
	if len(payload) > 1<<20 {
		t.Fatalf("input too large: %d", len(payload))
	}
	call.Name = ownerToolName("issues.create")
	call.Arguments, _ = json.Marshal(protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "issue"}, Payload: payload})
	value, err := e.server.MainOwnerTools().Call(t.Context(), call)
	if err != nil {
		t.Fatal(value, err)
	}
	data, _ := json.Marshal(value)
	var response protocol.APIResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Operation == nil {
		t.Fatalf("valid payload (%d bytes) committed %d issue(s) but lost receipt; tool success=true truncated=%v", len(payload), e.count(`SELECT COUNT(*) FROM issue_controls`), value.(map[string]any)["truncated"])
	}
}
