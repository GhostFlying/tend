package mainthread

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestOwnerSourceCannotSelectUnconsumedOrSystemMessage(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return ""
	}
	e := newEnv(t, h, withSteer(h, func(context.Context, harness.Thread, string, harness.TurnInput) error { return nil }))
	e.say("original")
	th := <-started
	future := e.say("not yet consumed")
	eventually(t, "native accepted but not consumed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted'`, future) == 1
	})
	e.m.mu.Lock()
	id := e.m.active.turnID
	e.m.mu.Unlock()
	args, _ := json.Marshal(map[string]any{"title": "must not create", "spec": spec, "commit_seq": 1, "source_message_id": future})
	result := e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: id, CallID: "unconsumed-source", Tool: "tasks_create", Arguments: args})
	if result.Success || e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("unconsumed owner source gained authority")
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "system-source", Author: "system", Body: "background suggestion", CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]any{"title": "must not create", "spec": spec, "commit_seq": 1, "source_message_id": "system-source"})
	result = e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: id, CallID: "invalid-source", Tool: "tasks_create", Arguments: args})
	if result.Success || e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("non-owner source gained authority")
	}
}

func TestHistoricalOwnerAnswerKeepsCurrentLogicalExecution(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	original := e.say("Which scope should apply?")
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
	var decision struct {
		ID string `json:"decision_id"`
	}
	if err := json.Unmarshal(e.exec(principal, "decisions.open", protocol.Target{Type: "decision"}, map[string]any{"question": "Which scope?", "options": []string{"keep scope", "stop scope"}, "source_message_id": original}, app.OpenDecision{}), &decision); err != nil {
		t.Fatal(err)
	}
	var answer struct {
		ID string `json:"message_id"`
	}
	choice := protocol.DecisionChoice{DecisionID: decision.ID, Version: 1, Option: "keep scope"}
	if err := json.Unmarshal(e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "keep scope", ReplyTo: original, Decision: &choice}, app.SubmitMessage{}), &answer); err != nil {
		t.Fatal(err)
	}
	oldRequest := e.ids.New(protocol.PrefixRequest)
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: oldRequest, MessageID: answer.ID, ThreadID: "native", TurnID: "old-turn", Status: store.TurnCompleted}); err != nil {
		t.Fatal(err)
	}
	current := e.say("Resolve the earlier admitted answer")
	currentRequest := e.ids.New(protocol.PrefixRequest)
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: currentRequest, MessageID: current, ThreadID: "native", TurnID: "current-turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"decision_id": decision.ID, "version": 1, "option": "keep scope", "source_message_id": answer.ID})
	call := harness.ToolCall{ThreadID: "native", TurnID: "current-turn", CallID: "historical-answer", Tool: "decisions_resolve", Arguments: args}
	if result := e.m.onToolCall(t.Context(), call); !result.Success {
		t.Fatal(result.Text)
	}
	state, err := e.app.LogicalTurn(t.Context(), currentRequest)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range state.Messages {
		found = found || message.MessageID == answer.ID
	}
	if !found {
		t.Fatal("historical answer bypassed current logical review")
	}
	if e.count(`SELECT COUNT(*) FROM request_records WHERE operation='decisions.resolve_from_message' AND parent_request_id=?`, oldRequest) != 1 {
		t.Fatal("historical answer lost its original audit parent")
	}
}

func TestNamedMainRetainsHostedOwnerSourceSelector(t *testing.T) {
	for _, spec := range tools.NamedSpecs(agentconfig.Main) {
		if spec.Name != "decisions_resolve" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if _, ok := schema.Properties["source_message_id"]; !ok {
			t.Fatal("named owner-answer tool lost earlier admitted source selector")
		}
		return
	}
	t.Fatal("missing decisions_resolve")
}
