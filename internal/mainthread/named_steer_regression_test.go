package mainthread

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

type namedSteerSession struct{ steerSession }

func (s namedSteerSession) Models(ctx context.Context) ([]harness.Model, error) {
	return s.Session.(harness.ModelCatalog).Models(ctx)
}

func TestNamedPlannerReconcilesSteerBeforeIssueCallback(t *testing.T) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "named-model", Name: "native-named", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return "completed"
	}
	inputs := make(chan harness.TurnInput, 1)
	e := newNamedSteerFixture(t, h, inputs)
	e.say("Retain the original planner request")
	var th *testkit.FakeThread
	select {
	case th = <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("named turn did not start")
	}
	var turnID, execution string
	eventually(t, "named native turn recorded", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		if e.m.active != nil {
			turnID, execution = e.m.active.turnID, e.m.active.requestID
		}
		return turnID != "" && execution != ""
	})
	supplement := e.say("Also create an Issue that I will handle myself")
	var input harness.TurnInput
	select {
	case input = <-inputs:
	case <-time.After(10 * time.Second):
		t.Fatal("native owner steer not delivered")
	}
	eventually(t, "steer accepted without consumed item", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted' AND native_item_id=''`, supplement) == 1
	})
	var contentRevision int64
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT content_revision FROM message_dispositions WHERE message_id=?`, supplement).Scan(&contentRevision)
	}); err != nil {
		t.Fatal(err)
	}
	payload := protocol.CreateIssuePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: supplement, ExpectedSourceRevision: &contentRevision}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Plan: protocol.IssuePlan{Title: "Owner's additional responsibility", Project: "default", Intent: "Record exactly the consumed owner supplement"}}
	args, _ := json.Marshal(map[string]any{"effect_seq": 1, "payload": payload})
	result := e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: turnID, CallID: "steered-issue", Tool: "issues_create", Arguments: args, UserText: input.Text, UserItemID: "native-supplement"})
	if !result.Success {
		t.Fatal("callback before user-item event lost admitted source: ", result.Text)
	}
	if e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed' AND native_item_id='native-supplement'`, supplement) != 1 || e.count(`SELECT COUNT(*) FROM hosted_coordination_effects WHERE execution_id=? AND effect_seq=1`, execution) != 1 {
		t.Fatal("consumed input replaced coordination identity or its receipt was lost")
	}
	if e.count(`SELECT COUNT(*) FROM issue_controls`) != 1 || e.count(`SELECT COUNT(*) FROM conversation_bindings WHERE message_id=? AND object_type='work_item'`, supplement) != 1 {
		t.Fatal("owner supplement did not create exactly one bound responsibility")
	}
}

// newNamedSteerFixture admits the same named planner and actual native callback path
// for source-consumption and durable coordination-identity regressions.
func newNamedSteerFixture(t *testing.T, h *testkit.Harness, inputs chan harness.TurnInput, options ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, h)
	e.stop()
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "NAMED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "named-model", Effort: "high", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	execute := func(principal protocol.Principal, operation string, target protocol.Target, payload any, command app.Command) {
		t.Helper()
		zero := int64(0)
		raw, _ := json.Marshal(payload)
		op, err := e.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: &zero}, command)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	execute(owner, "categories.put", protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, app.PutAgentCategory{})
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, app.AcceptHostedConfiguration{Prepared: prepared})
	e.app = app.New(e.store, e.ids, e.now, app.WithNotifier(e.signal), app.WithHostedConfiguration(prepared))
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	e.m = New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{Instance: "main", Thread: ThreadConfig(t.TempDir(), "legacy-unused"), OpenNamed: func(ctx context.Context, _ hostedconfig.Selection) (harness.Session, error) {
		session, err := h.Open(ctx)
		return namedSteerSession{steerSession{Session: session, steer: func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
			inputs <- in
			return nil
		}}}, err
	}, Log: quiet(), RestartDelay: 20 * time.Millisecond})
	for _, option := range options {
		option(&e.m.cfg)
	}
	e.start()

	return e
}
