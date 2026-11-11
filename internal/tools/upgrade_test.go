package tools

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"reflect"
	"testing"
)

type upgradeStarterProbe struct {
	inputs []protocol.UpgradeStartRequest
}

func (p *upgradeStarterProbe) Start(_ context.Context, r protocol.UpgradeStartRequest) (protocol.UpgradeView, error) {
	p.inputs = append(p.inputs, r)
	return protocol.UpgradeView{UpgradeID: r.UpgradeID, Revision: 1, Status: "running", Phase: "server_draining"}, nil
}

func TestUpgradeManagerParkAdmittedMainTool(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		found := false
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name == "upgrades_start" {
				found = true
			}
		}
		if found != (purpose == agentconfig.Main) {
			t.Fatal("wrong tool role", purpose, found)
		}
	}
	e := newEnv(t)
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 44)
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "test-hosted", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "test-model", Effort: "high", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.app = app.New(e.store, ids, clock.Now, app.WithHostedConfiguration(prepared))
	execute := func(principal protocol.Principal, cmd app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
		t.Helper()
		target, raw := composeFixtureBusinessInput(t, ids, cmd.Operation(), target, payload)
		op, err := e.svc.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: target, ExpectedControlRevision: revision, Payload: raw}, cmd)
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
		return op
	}
	execute(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "owner"}, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, new(int64))
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, new(int64))
	execute(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "owner"}, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Execute the exact approved deployment"}, nil)
	delivery, _, ok, err := e.svc.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	selection, named, err := e.svc.app.HostedSelectionForAdmission(t.Context(), delivery.RequestID, "main")
	if err != nil || !named {
		t.Fatal(named, err)
	}
	var payload app.HandleMessage
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: payload.MessageID, ThreadID: "main-upgrade-thread", Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model, Effort: selection.Definition.Effort}
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "native-upgrade-turn"
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	probe := &upgradeStarterProbe{}
	e.svc.SetUpgradeStarter(probe)
	args := protocol.UpgradeStartRequest{RequestID: ids.New(protocol.PrefixRequest), UpgradeID: "approved-upgrade", DeliveryTaskID: "approved-delivery-task", Release: "/verified/local-bundle", ParkIdleManagers: true}
	raw, _ := json.Marshal(args)
	call := Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: turn.ThreadID}, Native: "native-upgrade-call", TurnID: turn.TurnID, ExecutionRequestID: turn.RequestID, ParentRequestID: delivery.RequestID, Tool: "upgrades_start", Arguments: raw}
	result := e.svc.Handle(t.Context(), call)
	var answer answerView
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || !answer.OK {
		t.Fatal(result, err)
	}
	args.ExecutionRequestID = turn.RequestID
	if len(probe.inputs) != 1 || !reflect.DeepEqual(probe.inputs[0], args) {
		t.Fatal("typed admission changed", probe.inputs)
	}

	turn.Status = store.TurnCompleted
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	result = e.svc.Handle(t.Context(), call)
	if result.Success || len(probe.inputs) != 1 {
		t.Fatal("completed main execution initiated a new upgrade", result)
	}
	// The local controller remains the sole verifier of the exact approved bundle;
	// an invented/unadmitted execution cannot call it through the main tool name.
	call.ExecutionRequestID = "missing-execution"
	result = e.svc.Handle(t.Context(), call)
	if result.Success || len(probe.inputs) != 1 {
		t.Fatal("unadmitted tool reached controller", result, probe.inputs)
	}
}
