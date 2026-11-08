package mainthread

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Current consultation identities exercise shared pool and receipt boundaries
// without retaining an executable fixture for the retired work-item checker.
func createConsultationFixture(t *testing.T, e *env) protocol.AgentConversation {
	t.Helper()
	purposes := []agentconfig.Purpose{agentconfig.Main, agentconfig.Consultation}
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "adviser", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "HOSTED_EXECUTABLE", Enabled: true, DeclaredPurposes: purposes}}, Agents: []agentconfig.AgentDefinition{{LocalID: "adviser", Revision: 1, DisplayName: "Adviser", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: purposes, Model: "consult-model", Effort: "high", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	category, _ := json.Marshal(protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"})
	categoryOp, categoryErr := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "categories.put", Target: protocol.Target{Type: "category", ID: "planning"}, ExpectedControlRevision: &zero, Payload: category}, app.PutAgentCategory{})
	if categoryErr != nil || categoryOp.Error != nil {
		t.Fatal(categoryOp, categoryErr)
	}
	payload, _ := json.Marshal(map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
	op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "hosted.configure", Target: protocol.Target{Type: "hosted_configuration"}, ExpectedControlRevision: &zero, Payload: payload}, app.AcceptHostedConfiguration{Prepared: prepared})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	e.app = app.New(e.store, e.ids, e.now, app.WithNotifier(e.signal), app.WithHostedConfiguration(prepared))
	raw := e.exec(owner, "conversation.consult", protocol.Target{Type: "conversation"}, protocol.CreateConsultationPayload{AgentRef: "hosted/adviser", Title: "Current consultation"}, app.CreateConsultation{})
	var conversation protocol.AgentConversation
	if err := json.Unmarshal(raw, &conversation); err != nil {
		t.Fatal(err)
	}
	return conversation
}
