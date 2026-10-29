package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func hostedConfigurationFixture() hostedconfig.Configuration {
	return hostedconfig.Configuration{Revision: 1, EnvironmentID: "server-hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "hosted", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "traex-installation", DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Personal planner", RuntimeID: "hosted", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}, Model: "named-model", Effort: "high", Instructions: "PRIVATE_NAMED_ROLE_INSTRUCTIONS", Enabled: true}}}}
}
func acceptHostedFixture(t *testing.T, h *harness, c hostedconfig.Configuration, expected int64) (protocol.Operation, *hostedconfig.Prepared) {
	t.Helper()
	prepared, err := hostedconfig.Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("hosted.configure", protocol.Target{Type: "hosted_configuration"}, payload, rev(expected)), AcceptHostedConfiguration{Prepared: prepared})
	return op, prepared
}
func TestHostedConfigurationStartupAcceptanceIsVersionedAndPrivate(t *testing.T) {
	h := newHarness(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := hostedConfigurationFixture()
	op, first := acceptHostedFixture(t, h, c, 0)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	public, err := h.app.HostedAgentInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PRIVATE_NAMED") || strings.Contains(string(raw), "traex-installation") {
		t.Fatal("private configuration leaked into the public projection")
	}
	before := h.count("events")
	op, _ = acceptHostedFixture(t, h, c, 1)
	if op.Status != protocol.OpSucceeded || h.count("hosted_configuration_versions") != 1 || h.count("events") != before {
		t.Fatal("same revision/hash restart was not idempotent", op)
	}
	c.Definitions.Agents[0].Model = "other-model"
	conflicting, err := hostedconfig.Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": conflicting.Revision(), "hash": conflicting.Hash()}, rev(1)), AcceptHostedConfiguration{Prepared: conflicting})
	if protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatalf("same revision changed contents: %v", err)
	}
	c.Revision = 2
	op, _ = acceptHostedFixture(t, h, c, 1)
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	c.Definitions.Agents[0].Revision = 2
	op, _ = acceptHostedFixture(t, h, c, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old, err := h.app.HostedConfiguration(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	original, err := old.Select("planner", agentconfig.Main)
	if err != nil {
		t.Fatal(err)
	}
	if old.Hash() != first.Hash() || original.Definition.Model != "named-model" || original.Definition.Instructions != "PRIVATE_NAMED_ROLE_INSTRUCTIONS" {
		t.Fatal("new definition rewrote accepted private history")
	}
	current, err := h.app.HostedAgentInventory(t.Context())
	if err != nil || current.ConfigurationRevision != 2 {
		t.Fatalf("current registration=%+v %v", current, err)
	}
}

func TestHostedNamedSettingsAndExecutionKeepOneDefinitionSource(t *testing.T) {
	h := newHarness(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	legacy := protocol.SetConversationSettingsPayload{Model: "legacy-model", Effort: "low"}
	setter := SetConversationSettings{Available: true, Models: []protocol.ConversationModel{{ID: "legacy-model", DefaultEffort: "low", SupportedEfforts: []string{"low"}}}}
	if op := h.exec(owner, h.env("conversation.set_settings", protocol.Target{Type: "conversation"}, legacy, rev(0)), setter); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := hostedConfigurationFixture()
	op, prepared := acceptHostedFixture(t, h, c, 0)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	selected, named, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || !named || selected.Definition.Model != "named-model" {
		t.Fatalf("named selection=%+v %t %v", selected, named, err)
	}
	expectWorkCode(t, h.exec(owner, h.env("conversation.set_settings", protocol.Target{Type: "conversation"}, legacy, rev(1)), setter), protocol.CodeInvalidTransition)
	oldSettings, err := h.app.ConversationSettings(t.Context())
	if err != nil || oldSettings.Model != "legacy-model" || oldSettings.Revision != 1 {
		t.Fatal("named mode rewrote historical legacy settings", oldSettings, err)
	}
	message := queueMessage(t, h, "investigate the owner goal")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("delivery %t %v", ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: message, ThreadID: "native-hosted", Status: store.TurnStarting, Model: selected.Definition.Model, Effort: selected.Definition.Effort, Hosted: &selected}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	pinned, found, err := h.app.HostedExecution(t.Context(), d.RequestID)
	if err != nil || !found || pinned.Selection.ConfigurationHash != prepared.Hash() {
		t.Fatal("execution snapshot missing", pinned, found, err)
	}
	turn.Status = store.TurnStarted
	turn.TurnID = "native-failed"
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnFailed
	turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := h.app.AgentRecovery(t.Context(), d.RequestID)
	if err != nil || !found {
		t.Fatal(recovery, found, err)
	}
	c.Revision = 2
	c.Definitions.Agents[0].Revision = 2
	c.Definitions.Agents[0].Model = "future-model"
	c.Definitions.Agents[0].Instructions = "FUTURE_PRIVATE_INSTRUCTIONS"
	op, next := acceptHostedFixture(t, h, c, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(next))
	current, named, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || !named || current.Definition.Model != "future-model" {
		t.Fatal(current, named, err)
	}
	continuation, named, err := h.app.HostedSelectionForAdmission(t.Context(), recovery.ActiveRequestID, "main")
	if err != nil || !named || continuation.ConfigurationHash != prepared.Hash() || continuation.Definition.Model != "named-model" || continuation.Definition.Instructions != "PRIVATE_NAMED_ROLE_INSTRUCTIONS" {
		t.Fatal("continuation reselected current definition", continuation, named, err)
	}
}

func TestHostedAssignmentFencesCoordinationWithoutStoppingPrimaryWorker(t *testing.T) {
	h := newHarness(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	run, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no primary worker claim")
	}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	target := protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"}
	payload := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: target, Reason: "Delegate this concrete issue while its frozen worker continues"}
	op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), HostedIssueAssignment{})
	next := canonicalLifecycleIssue(t, op)
	if next.AssignmentEpoch != issue.AssignmentEpoch+1 || next.AssignmentState != "handoff" || next.Assignee != issue.Assignee {
		t.Fatal("assignment skipped the durable fence", next)
	}
	stale := canonicalOwnerIssueUpdate{Plan: next.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	expectWorkCode(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, stale, rev(next.Revision)), CanonicalOwnerIssueCommand{Action: "update"}), protocol.CodeStaleRevision)
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "No old manager native/control operation is outstanding"}
	next = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
	if next.Assignee != target || next.AssignmentState != "active" {
		t.Fatal(next)
	}
	if current := h.task(task); current.Lifecycle != protocol.TaskRunning {
		t.Fatal("handoff stopped the independent worker", current)
	}
	var desired string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT desired_state FROM runs WHERE run_id=?`, run.RunID).Scan(&desired)
	}); err != nil {
		t.Fatal(err)
	}
	if desired != string(protocol.DesiredRun) {
		t.Fatal("handoff changed the frozen run", desired)
	}
	selected, named, err := h.app.NamedHostedSelection(t.Context(), "issue:"+issue.IssueID)
	if err != nil || !named || selected.Purpose != agentconfig.IssueManager {
		t.Fatal(selected, named, err)
	}
	before := h.count("events")
	payload.ExpectedAssignmentEpoch = next.AssignmentEpoch
	replay := h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(next.Revision))
	unchanged := canonicalLifecycleIssue(t, h.exec(owner, replay, HostedIssueAssignment{}))
	if unchanged.Revision != next.Revision || unchanged.AssignmentEpoch != next.AssignmentEpoch || h.count("events") != before {
		t.Fatal("same-assignee request had a business effect")
	}
}
