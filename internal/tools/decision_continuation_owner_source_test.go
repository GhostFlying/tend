package tools

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestDecisionContinuationManagerCallbackRequiresUnwithdrawnOwnerSource(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		name := "admitted"
		if withdrawn {
			name = "withdrawn"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			clock := testkit.NewClock()
			ids := testkit.NewIDGen(clock, 96)
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test-owner"}
			execute := func(principal protocol.Principal, cmd app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				op, err := e.svc.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: target, ExpectedControlRevision: revision, Payload: raw}, cmd)
				if err != nil || op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
					t.Fatal(cmd.Operation(), op, err)
				}
				return op
			}
			execute(owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, new(int64))
			prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1,
				Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "test-hosted", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}},
				Agents:   []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "test-model", Enabled: true}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			execute(protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, new(int64))
			e.svc.app = app.New(e.store, ids, clock.Now, app.WithHostedConfiguration(prepared))
			op := execute(owner, app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Original responsibility", Project: "default", Intent: "Resolve the worker question"}}, nil)
			var issue protocol.Issue
			if err := json.Unmarshal(op.Result, &issue); err != nil {
				t.Fatal(err)
			}
			op = execute(owner, app.HostedIssueAssignment{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/planner"}, Reason: "Coordinate the original task"}, &issue.Revision)
			if err := json.Unmarshal(op.Result, &issue); err != nil {
				t.Fatal(err)
			}
			op = execute(owner, app.HostedIssueAssignment{Finish: true}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "No previous manager"}, &issue.Revision)
			if err := json.Unmarshal(op.Result, &issue); err != nil {
				t.Fatal(err)
			}
			runtime := &protocol.RunnerRuntime{InstanceID: "question-runner", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs}}
			runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}
			execute(runner, app.Hello{}, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil)
			op = execute(owner, app.CanonicalOwnerTaskCreate{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Original worker", Spec: protocol.OwnerTaskSpec{Goal: "Implement the bounded change", Acceptance: []string{"The original answer is followed"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}, &issue.Revision)
			var created struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(op.Result, &created); err != nil || created.TaskID == "" {
				t.Fatal(op, err)
			}
			op = execute(runner, app.Claim{}, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil)
			var claim struct {
				Assignment *protocol.Assignment `json:"assignment"`
			}
			if err := json.Unmarshal(op.Result, &claim); err != nil || claim.Assignment == nil || claim.Assignment.TaskID != created.TaskID || !claim.Assignment.QuestionHandoffs {
				t.Fatal("original worker was not admitted", op, err)
			}
			as := claim.Assignment
			worker := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
			target := protocol.Target{Type: "run", ID: as.RunID}
			execute(worker, app.ReportRunStarted{}, target, nil, nil)
			op = execute(worker, app.AskOwner{}, target, protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Continue the original work?", Options: []string{"continue"}}, nil)
			var question protocol.WorkerQuestion
			if err := json.Unmarshal(op.Result, &question); err != nil {
				t.Fatal(err)
			}
			execute(worker, app.YieldQuestion{}, target, protocol.YieldQuestionPayload{DecisionID: question.DecisionID, ThreadID: "original-worker-thread"}, nil)

			conversation := "issue:" + issue.IssueID
			execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Continue the original task"}, nil)
			delivery, _, ok, err := e.svc.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			selection, named, err := e.svc.app.HostedSelectionForAdmission(t.Context(), delivery.RequestID, conversation)
			if err != nil || !named {
				t.Fatal(named, err)
			}
			var input app.HandleMessage
			if err := json.Unmarshal(delivery.Payload, &input); err != nil {
				t.Fatal(err)
			}
			turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: input.MessageID, ThreadID: "manager-thread", Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model}
			if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			turn.Status, turn.TurnID = store.TurnStarted, "manager-turn"
			if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			source := input.MessageID
			if withdrawn {
				// A different live manager turn remains active while the owner
				// withdraws this not-yet-claimed input through the real command.
				op := execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "A subsequently withdrawn answer"}, nil)
				var queued struct {
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(op.Result, &queued); err != nil {
					t.Fatal(err)
				}
				source = queued.MessageID
				var control protocol.MessageControl
				if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					control, err = store.MessageControl(t.Context(), tx.Q(), source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				execute(owner, app.WithdrawMessage{}, protocol.Target{Type: "message", ID: source}, protocol.WithdrawMessagePayload{}, &control.Revision)
			}
			var task store.TaskRow
			if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				task, err = store.GetTask(t.Context(), tx.Q(), created.TaskID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(protocol.AnswerDecisionPayload{Version: question.Version, Option: "continue", SourceMessageID: source, Continuation: &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionContinueNow}})
			args, _ := json.Marshal(hostedMutationArgs{TargetID: question.DecisionID, ExpectedControlRevision: &task.ControlRevision, EffectSeq: 1, Payload: payload})
			principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-manager", Thread: turn.ThreadID, ConversationID: conversation}
			call := Call{Principal: principal, Native: "original-manager-answer-callback", RequestID: ids.New(protocol.PrefixRequest), TurnID: turn.TurnID, ExecutionRequestID: turn.RequestID, ParentRequestID: delivery.RequestID, Tool: "decision_continuation_answer", Arguments: args}
			first := e.svc.Handle(t.Context(), call)
			var answer answerView
			if err := json.Unmarshal([]byte(first.Text), &answer); err != nil {
				t.Fatal(first.Text, err)
			}
			if withdrawn {
				if answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeForbidden {
					t.Fatal("withdrawn owner input authorized typed continuation", first.Text)
				}
			} else if !answer.OK {
				t.Fatal("admitted owner answer was rejected", first.Text)
			}
			again := e.svc.Handle(t.Context(), call)
			if !reflect.DeepEqual(first, again) {
				t.Fatal("original callback receipt changed", first, again)
			}
			if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				d, err := store.GetDecision(t.Context(), tx.Q(), question.DecisionID)
				if err != nil {
					return err
				}
				policy, err := store.GetDecisionContinuation(t.Context(), tx.Q(), question.DecisionID)
				if err != nil {
					return err
				}
				var actions, effects int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM plan_actions WHERE effect_key=?`, "question_continuation|"+question.DecisionID).Scan(&actions); err != nil {
					return err
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM local_hosted_coordination_effects WHERE request_id=?`, call.RequestID).Scan(&effects); err != nil {
					return err
				}
				receipt, err := store.GetRequest(t.Context(), tx.Q(), principal.DedupDomain(), call.RequestID)
				if withdrawn {
					// Authorization rejections roll back the whole transaction;
					// they do not create a recorded business-failure receipt.
					if d.Status != store.DecisionOpen || d.AnswerJSON != "" || policy != nil || actions != 0 || effects != 0 || !errors.Is(err, store.ErrNotFound) {
						t.Fatal("rejected callback changed original facts", d, policy, actions, effects, receipt)
					}
					return nil
				}
				if err != nil {
					return err
				}
				if d.Status != store.DecisionAnswered || policy == nil || actions != 1 || effects != 1 || receipt.Status != protocol.OpSucceeded {
					t.Fatal("valid callback did not commit exactly once", d, policy, actions, effects, receipt)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
