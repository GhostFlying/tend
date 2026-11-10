package runnerapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerExternalWaitHTTPKeepsOriginalIdentityAndReceipts(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	execute := func(command app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
		t.Helper()
		env := e.envelope(command.Operation(), target.ID, payload)
		env.Target, env.ExpectedControlRevision = target, revision
		op, err := e.app.Execute(t.Context(), owner, env, command)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(command.Operation(), op, err)
		}
		return op
	}
	execute(app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "execution"}, protocol.PutAgentCategoryPayload{Title: "Execution", State: "active"}, new(int64))
	runtime := &protocol.RunnerRuntime{InstanceID: "wait-http", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityAgentInventory, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityExternalWaits}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}))
	directory, err := agentconfig.Validate("linux-1", agentconfig.Config{FormatVersion: 2,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "native", Revision: 1, Kind: agentconfig.AppServerWorker, CLI: "codex", ExecutableRef: "private-installation", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun, agentconfig.IssueManager}, Enabled: true}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "owner", Revision: 1, DisplayName: "Owner", RuntimeID: "native", Categories: []string{"execution"}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "model", Enabled: true}},
	}, agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: "private-source"})
	if err != nil {
		t.Fatal(err)
	}
	projection := directory.Public()
	_, hash, err := directory.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	register := e.envelope(protocol.OpRunnerInventoryRegister, "linux-1", protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: "wait-snapshot", PublicSHA256: hash, Projection: projection})
	register.Target.Type, register.ExpectedControlRevision = "host", new(int64)
	e.mustOK(asHost(), register)
	a := projection.Agents[0]
	runtime.JournalVersion = 22
	runtime.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{Purpose: "issue_manager", Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "wait-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel}, Worker: protocol.WorkerAppServer}}}
	runtime.Sequence++
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}))
	var issue protocol.Issue
	load := func(op protocol.Operation) {
		t.Helper()
		if err := json.Unmarshal(op.Result, &issue); err != nil {
			t.Fatal(err)
		}
	}
	load(execute(app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Wait for existing work", Project: "default", Intent: "Resume the same owner responsibility"}}, nil))
	load(execute(app.RequestIssueManager{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "execution", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Select the configured executor"}, &issue.Revision))
	load(execute(app.CanonicalOwnerIssueCommand{Action: "set_status"}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.SetIssueStatusPayload{Status: protocol.IssueTodo, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, &issue.Revision))
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "linux-1", protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}))
	if as == nil || as.Role != protocol.RoleIssueManager || !as.ExternalWaits {
		t.Fatal("no real owner assignment", as)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		issue, err = store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	created := execute(app.CanonicalOwnerTaskCreate{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Existing investigation", Spec: protocol.OwnerTaskSpec{Goal: "Investigate the result", Acceptance: []string{"Retain the evidence"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}, &issue.Revision)
	var dependency struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Result, &dependency); err != nil {
		t.Fatal(err)
	}
	waitRequest := e.envelope(protocol.OpRunsWaitExternal, as.RunID, protocol.WaitExternalPayload{WaitKey: "existing-task", Kind: "task", TargetID: dependency.TaskID, Reason: "Wait for the existing investigation", Recovery: "Read its result and decide the next action"})
	var wait protocol.ExternalWait
	if err := json.Unmarshal(e.mustOK(asRun(*as), waitRequest), &wait); err != nil || wait.RunID != as.RunID || wait.TaskID != as.TaskID || wait.Stopped {
		t.Fatal("manager wait did not reach its original execution", wait, err)
	}
	// The HTTP identity must still forbid another Run's physical facts.
	if status, result := e.call(asRun(*as), e.envelope(protocol.OpRunsRecordSessionStop, e.ids.New(protocol.PrefixRun), protocol.SessionStopPayload{ThreadID: "original-thread"})); status != http.StatusForbidden || result.Error == nil {
		t.Fatal("manager recorded another execution's close", status, result)
	}
	stopRequest := e.envelope(protocol.OpRunsRecordSessionStop, as.RunID, protocol.SessionStopPayload{ThreadID: "original-thread"})
	stopReceipt := e.mustOK(asRun(*as), stopRequest)
	yieldRequest := e.envelope(protocol.OpRunsYieldExternalWait, as.RunID, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID, ThreadID: "original-thread"})
	yieldReceipt := e.mustOK(asRun(*as), yieldRequest)
	if replay := e.mustOK(asRun(*as), yieldRequest); string(replay) != string(yieldReceipt) {
		t.Fatal("released lease lost its exact yield receipt")
	}
	if replay := e.mustOK(asRun(*as), stopRequest); string(replay) != string(stopReceipt) {
		t.Fatal("released lease lost its exact physical-close receipt")
	}
	waits, err := e.app.ExternalWaits(t.Context(), as.TaskID)
	if err != nil || len(waits) != 1 || !waits[0].Stopped || waits[0].Status != "waiting" {
		t.Fatal("HTTP stop did not persist the external wait", waits, err)
	}
}
