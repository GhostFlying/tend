package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
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
	"github.com/GhostFlying/tend/internal/testkit/issuefixture"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestNamedPoolKeepsCompletionAcrossOwnerDefinitionChanges(t *testing.T) {
	testNamedPoolAdmissionProgress(t, false)
}
func TestNamedPoolDisabledCurrentDefinitionDoesNotConsumeNewInput(t *testing.T) {
	testNamedPoolAdmissionProgress(t, true)
}
func TestNamedPoolUnavailableOwnerPreservesCompletedHistory(t *testing.T) {
	testNamedPoolAdmissionProgress(t, false, true)
}
func testNamedPoolAdmissionProgress(t *testing.T, disable bool, deferred ...bool) {
	deferStartup := len(deferred) > 0 && deferred[0]
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "v1", Name: "v1", DefaultEffort: "high", SupportedEfforts: []string{"high"}}, {ID: "v2", Name: "v2", DefaultEffort: "high", SupportedEfforts: []string{"high"}}}
	e := newEnv(t, h)
	e.stop()
	execute := func(principal protocol.Principal, operation string, target protocol.Target, payload any, revision int64, command app.Command) protocol.Operation {
		t.Helper()
		target, raw := positiveFixtureInput(t, e.ids, operation, target, payload)
		expected := &revision
		if operation == "issues.create" || operation == "conversation.submit_message" {
			expected = nil
		}
		op, err := e.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: expected}, command)
		if err != nil || op.Status == protocol.OpFailed {
			t.Fatal(operation, op, err)
		}
		return op
	}
	decodeIssue := func(op protocol.Operation) protocol.Issue {
		t.Helper()
		var issue protocol.Issue
		if err := json.Unmarshal(op.Result, &issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	execute(owner, "categories.put", protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, 0, app.PutAgentCategory{})
	config := hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "hosted-executable", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "v1", Effort: "high", Enabled: true}}}}
	accept := func(expected int64) {
		t.Helper()
		p, err := hostedconfig.Prepare(config)
		if err != nil {
			t.Fatal(err)
		}
		execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": p.Revision(), "hash": p.Hash()}, expected, app.AcceptHostedConfiguration{Prepared: p})
		e.app = app.New(e.store, e.ids, e.now, app.WithMainOwner(owner.Subject), app.WithHostedConfiguration(p))
	}
	accept(0)
	issue := decodeIssue(execute(owner, "issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Investigate", Project: "default", Intent: "Delegate investigation"}}, 0, app.CanonicalOwnerIssueCommand{Action: "create"}))
	retained, fixtureErr := issuefixture.Retained(t.Context(), e.store, e.ids, e.now(), issue.IssueID, "")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	issue = retained
	issue = decodeIssue(execute(owner, "issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/planner"}, Reason: "Delegate investigation"}, issue.Revision, app.HostedIssueAssignment{}))
	issue = decodeIssue(execute(owner, "issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "No previous execution"}, issue.Revision, app.HostedIssueAssignment{Finish: true}))
	conversation := "issue:" + issue.IssueID
	submit := func(body string) string {
		op := execute(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: body}, 0, app.SubmitMessage{})
		var result struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil || result.MessageID == "" {
			t.Fatal("submitted owner source", op, err)
		}
		return result.MessageID
	}
	newManager := func(id string) *Manager {
		cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
		return New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{ConversationID: id, Instance: id, Thread: ThreadConfig(t.TempDir(), "unused"), Now: e.now, RestartDelay: 10 * time.Millisecond, Log: quiet(), OpenNamed: func(ctx context.Context, _ hostedconfig.Selection) (harness.Session, error) { return h.Open(ctx) }})
	}
	// A real first native turn completes without source-review bookkeeping.
	// Changing configuration must neither replay it nor create a model repair.
	h.Turn = func(_ *testkit.FakeThread, _ string, _ harness.TurnInput) string { return "completed" }
	submit("initial goal")
	original, _, ok, err := e.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(original, ok, err)
	}
	m := newManager(conversation)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	result := m.Deliver(ctx, original)
	if result.Outcome != app.Delivered {
		cancel()
		<-done
		t.Fatal(result)
	}
	if err = e.app.FinishDelivery(t.Context(), original, result); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	cancel()
	<-done
	var originalThread string
	if err = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT thread_id FROM main_turns WHERE request_id=?`, original.RequestID).Scan(&originalThread)
	}); err != nil {
		t.Fatal(err)
	}
	newOwnerMessage := submit("new owner goal")
	config.Revision = 2
	config.Definitions.Agents[0].Revision = 2
	config.Definitions.Agents[0].Model = "v2"
	if deferStartup {
		config.Definitions.Agents[0].Model = "unavailable-new-model"
	}
	if disable {
		replacement := config.Definitions.Agents[0]
		replacement.LocalID = "replacement-main"
		config.Definitions.Agents = append(config.Definitions.Agents, replacement)
		config.MainAgentID = "replacement-main"
		config.Definitions.Agents[0].Enabled = false
	}
	accept(1)
	var v1Repairs, v2Inputs atomic.Int32
	failures := make(chan string, 8)
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if in.Repair && in.Model == "v1" {
			v1Repairs.Add(1)
		}
		if !in.Repair && in.Model == "v2" {
			v2Inputs.Add(1)
		}
		invoke := func(name string, args any, out any) bool {
			r := th.CallTool(context.Background(), turn, name, name, args)
			var response struct {
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
			}
			if !r.Success || json.Unmarshal([]byte(r.Text), &response) != nil || !response.OK {
				failures <- fmt.Sprintf("%s: %s", name, r.Text)
				return false
			}
			if out != nil && json.Unmarshal(response.Result, out) != nil {
				failures <- "invalid response"
				return false
			}
			return true
		}
		var item protocol.IssueView
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}

		return "completed"
	}
	var scans atomic.Int32
	pool := &BackgroundPool{App: e.app, Limit: 1, scanned: func() { scans.Add(1) }, New: func(id string) (*Manager, error) { return newManager(id), nil }}
	// One bounded pool run verifies current admission and immutable completed history.
	phaseStarted := time.Now()
	run, stop := context.WithTimeout(t.Context(), 30*time.Second)
	finished := make(chan struct{})
	go func() { defer close(finished); pool.Run(run) }()
	defer func() { stop(); <-finished }()
	var repaired, current protocol.LogicalTurnState
	var readErr error
	for {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		default:
		}
		repaired, readErr = e.app.LogicalTurn(run, original.RequestID)
		currentDone := false
		if disable || deferStartup {
			currentDone = scans.Load() >= 1
		} else {
			var nextRequest string
			err := e.store.Read(run, func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(run, `SELECT request_id FROM main_turns WHERE message_id=? AND status='completed' ORDER BY rowid DESC LIMIT 1`, newOwnerMessage).Scan(&nextRequest)
			})
			if err == nil {
				current, err = e.app.LogicalTurn(run, nextRequest)
				currentDone = err == nil && current.Status == "completed" && v2Inputs.Load() > 0
			}
		}
		if readErr == nil && repaired.Status == "completed" && v1Repairs.Load() == 0 && currentDone {
			break
		}

		select {
		case <-run.Done():
			t.Fatalf("completed v1 or current owner admission failed: repair=%s/%s count=%d owner=%s/%s starts=%d read=%v phase=%v", repaired.ID, repaired.Status, v1Repairs.Load(), current.ID, current.Status, v2Inputs.Load(), readErr, run.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Logf("preserved v1 completion logical=%s status=%s count=%d; owner v2 source=%s logical=%s status=%s starts=%d disabled=%t deferred=%t phase_elapsed=%s", repaired.ID, repaired.Status, v1Repairs.Load(), newOwnerMessage, current.ID, current.Status, v2Inputs.Load(), disable, deferStartup, time.Since(phaseStarted))
	if e.count(`SELECT COUNT(*) FROM logical_turn_executions WHERE repair=1`) != 0 || v1Repairs.Load() != 0 {
		t.Fatal("definition change created a source-only model repair")
	}
	if (disable || deferStartup) && v2Inputs.Load() != 0 {
		t.Fatal("disabled current manager consumed new business", v2Inputs.Load())
	}
	if n := e.count(`SELECT COUNT(*) FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE t.thread_id=? AND h.configuration_revision<>1`, originalThread); n != 0 {
		t.Fatal("old runtime rebound to a new selection", n)
	}
}
