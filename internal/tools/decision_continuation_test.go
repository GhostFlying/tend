package tools

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestDecisionContinuationNamedToolsExposeBothCASAndAdmittedSource(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		found := map[string]bool{}
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name != "decision_continuation_read" && !decisionContinuationMutation(spec.Name) {
				continue
			}
			found[spec.Name] = true
			var root map[string]any
			if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
				t.Fatal(err)
			}
			if spec.Name == "decision_continuation_read" {
				if !slices.Contains(root["required"].([]any), any("decision_id")) {
					t.Fatal("continuation read omitted original identity", root)
				}
				continue
			}
			for _, field := range []string{"target_id", "expected_control_revision", "effect_seq", "payload"} {
				if !slices.Contains(root["required"].([]any), any(field)) {
					t.Fatal("mutation omitted admission field", purpose, spec.Name, field)
				}
			}
			if minimum := planningSchemaAt(t, root, "effect_seq")["minimum"]; minimum != float64(1) {
				t.Fatal("mutation lost per-execution effect ordering", minimum)
			}
			payload := planningSchemaAt(t, root, "payload")
			fields := []string{"expected_revision"}
			if spec.Name == "decision_continuation_answer" {
				fields = []string{"version", "continuation"}
			}
			if spec.Name == "decision_continuation_resume" || spec.Name == "decision_continuation_answer" {
				fields = append(fields, "source_message_id")
			}
			for _, field := range fields {
				if !slices.Contains(payload["required"].([]any), any(field)) {
					t.Fatal("mutation omitted continuation CAS or actual source", spec.Name, field)
				}
			}
			if spec.Name == "decision_continuation_reconcile" && slices.Contains(payload["required"].([]any), any("source_message_id")) {
				t.Fatal("machine reconciliation manufactures a new owner-source requirement")
			}
			if payload["properties"].(map[string]any)["ready"] != nil {
				t.Fatal("caller readiness claim became a public input")
			}
		}
		for _, name := range []string{"decision_continuation_read", "decision_continuation_answer", "decision_continuation_resume", "decision_continuation_reconcile"} {
			if found[name] != (purpose != agentconfig.Consultation) {
				t.Fatal("continuation capability escaped its responsibility", purpose, name, found)
			}
		}
	}
	var schema map[string]any
	for _, spec := range NamedSpecs(agentconfig.Main) {
		if spec.Name == "decision_continuation_answer" {
			if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	mode := planningSchemaAt(t, schema, "payload", "continuation", "mode")["enum"].([]any)
	if !slices.Equal(mode, []any{"continue_now", "wait", "dependency"}) {
		t.Fatal("typed answer tool lost modes", mode)
	}
	for _, spec := range NamedSpecs(agentconfig.Main) {
		if spec.Name != "attentions_respond" && spec.Name != "attentions_resolve" {
			continue
		}
		var root map[string]any
		if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
			t.Fatal(err)
		}
		modes := planningSchemaAt(t, root, "payload", "continuation", "mode")["enum"].([]any)
		if !slices.Equal(modes, mode) {
			t.Fatal("legal responsibility path lost typed continuation", spec.Name, modes)
		}
	}
	for _, name := range []string{"decision_continuation_resume", "decision_continuation_reconcile"} {
		if _, exists := schemas[name]; exists {
			t.Fatal("continuation mutation gained an unadmitted legacy dispatch surface", name)
		}
	}
}

func TestDecisionResolveToolRedirectsUnsupportedTypedPolicyWithoutDroppingIt(t *testing.T) {
	e := newEnv(t)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "msg_1", Author: "owner", Body: "Wait for an independent resume"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	opened := e.call("decisions_open", openArgs{Question: "Which scope?", Options: []string{"bounded"}}, "open-original")
	var created struct {
		Outcome struct {
			DecisionID string `json:"decision_id"`
		} `json:"outcome"`
	}
	if !opened.OK || json.Unmarshal(opened.Result, &created) != nil || created.Outcome.DecisionID == "" {
		t.Fatal(opened)
	}
	args := map[string]any{"decision_id": created.Outcome.DecisionID, "version": 1, "option": "bounded", "continuation": protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Await independent resume"}}
	first := e.call("decisions_resolve", args, "original-answer")
	// A non-worker decision cannot accept a worker handoff policy. If the tool
	// drops the new field, this call would silently succeed and answer it.
	if first.OK || first.Error == nil || first.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(first.Error.Message, "explicit typed") {
		t.Fatal("typed continuation was dropped before the real app handler", first)
	}
	replay := e.call("decisions_resolve", args, "original-answer")
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("original tool callback changed its failure receipt", first, replay)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		decision, err := store.GetDecision(t.Context(), tx.Q(), created.Outcome.DecisionID)
		if err == nil && (decision.Status != store.DecisionOpen || decision.AnswerJSON != "") {
			t.Fatal("failed typed answer changed original answer fact", decision)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionContinuationNamedDispatchUsesOriginalExecutionAndRequest(t *testing.T) {
	e := newEnv(t)
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 94)
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "test-hosted", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "test-model", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.app = app.New(e.store, ids, clock.Now, app.WithHostedConfiguration(prepared))
	execute := func(principal protocol.Principal, cmd app.Command, target protocol.Target, payload any, revision *int64) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		op, err := e.svc.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: target, ExpectedControlRevision: revision, Payload: raw}, cmd)
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "owner"}
	execute(owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, new(int64))
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, new(int64))
	execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Independently resume the original answered question"}, nil)
	delivery, _, ok, err := e.svc.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	selection, named, err := e.svc.app.HostedSelectionForAdmission(t.Context(), delivery.RequestID, "main")
	if err != nil || !named {
		t.Fatal(named, err)
	}
	var input app.HandleMessage
	if err := json.Unmarshal(delivery.Payload, &input); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: input.MessageID, ThreadID: "continuation-thread", Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model, Effort: selection.Definition.Effort}
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "continuation-turn"
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: turn.ThreadID}
	call := Call{Principal: principal, Native: "original-continuation-read", TurnID: turn.TurnID, ExecutionRequestID: turn.RequestID, ParentRequestID: delivery.RequestID, Tool: "decision_continuation_read", Arguments: json.RawMessage(`{"decision_id":"missing"}`)}
	result := e.svc.Handle(t.Context(), call)
	var read answerView
	if err := json.Unmarshal([]byte(result.Text), &read); err != nil || read.OK || read.Error == nil || read.Error.Code != protocol.CodeNotFound {
		t.Fatal("named read did not reach its real handler", result, err)
	}
	for i, name := range []string{"decision_continuation_answer", "decision_continuation_resume", "decision_continuation_reconcile"} {
		call.Tool, call.Native, call.RequestID = name, "original-"+name, ids.New(protocol.PrefixRequest)
		payload := json.RawMessage(`{"expected_revision":0,"source_message_id":"` + input.MessageID + `"}`)
		operation := "decisions.continuation." + strings.TrimPrefix(name, "decision_continuation_")
		if name == "decision_continuation_answer" {
			operation = protocol.OpResolveDecisionContinuation
			payload = json.RawMessage(`{"version":1,"text":"Wait","continuation":{"mode":"wait","reason":"Await original dependency"},"source_message_id":"` + input.MessageID + `"}`)
		}
		call.Arguments, _ = json.Marshal(hostedMutationArgs{TargetID: "missing", ExpectedControlRevision: new(int64), EffectSeq: int64(i + 1), Payload: payload})
		first := e.svc.Handle(t.Context(), call)
		var receipt answerView
		if err := json.Unmarshal([]byte(first.Text), &receipt); err != nil || receipt.OK || receipt.Error == nil || receipt.Error.Code != protocol.CodeNotFound {
			t.Fatal("named continuation did not reach core admission", name, first, err)
		}
		again := e.svc.Handle(t.Context(), call)
		if first.Text != again.Text || first.Success != again.Success {
			t.Fatal("original native callback changed its receipt", first, again)
		}
		expected := protocol.Envelope{RequestID: call.RequestID, Operation: operation, Target: protocol.Target{Type: "decision", ID: "missing"}, ExpectedControlRevision: new(int64), Payload: payload}
		wantHash, err := protocol.PayloadHash(&expected)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			record, err := store.GetRequest(t.Context(), tx.Q(), principal.DedupDomain(), call.RequestID)
			if err == nil && (record.Operation != expected.Operation || record.PayloadHash != wantHash || record.Status != protocol.OpFailed || record.ErrorCode != string(protocol.CodeNotFound)) {
				t.Fatal("named dispatch changed the original request or CAS intent", record, expected)
			}
			return err
		}); err != nil {
			t.Fatal("original tool request was not recoverable", err)
		}
	}
	call.Tool, call.Principal.Thread = "decision_continuation_read", "another-thread"
	call.Arguments = json.RawMessage(`{"decision_id":"missing"}`)
	result = e.svc.Handle(t.Context(), call)
	var denied answerView
	if err := json.Unmarshal([]byte(result.Text), &denied); err != nil || denied.OK || denied.Error == nil || denied.Error.Code != protocol.CodeForbidden {
		t.Fatal("another native thread bypassed execution ownership", result, err)
	}
	result = e.svc.Handle(t.Context(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalRun, Subject: "unadmitted-manager", Role: protocol.RoleIssueManager}, Tool: "decision_continuation_read", Arguments: json.RawMessage(`{"decision_id":"missing"}`)})
	if result.Success || !strings.Contains(result.Text, `"code":"forbidden"`) {
		t.Fatal("manager read fell through without persisted execution", result)
	}
}
