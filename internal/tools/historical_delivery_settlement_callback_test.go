package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/testkit/issuefixture"
	"github.com/GhostFlying/tend/internal/views"
)

type historicalHostedDelivery struct {
	e         *env
	ids       protocol.IDGen
	clock     *testkit.Clock
	owner     protocol.Principal
	principal protocol.Principal
	turn      store.MainTurn
	issue     protocol.Issue
	source    string
	execution string
	original  protocol.IssueDeliveryDuty
	batch     store.UpgradeRow
}

func (f *historicalHostedDelivery) putObservation(tx *store.Tx, ctx context.Context, row store.DeliveryObservationRow) error {
	fact, err := tx.PutDeliveryObservationFact(ctx, f.issue.Plan.Deliveries[0], row.Observation)
	if err != nil {
		return err
	}
	row.QueryBaselineUpgradeID = row.Observation.BaselineUpgradeID
	row.QueryBaselineUpgradeRevision = row.Observation.BaselineUpgradeRevision
	row.ObservedRevision = fact.FactRevision
	ref := f.issue.Plan.Deliveries[0]
	if _, err := tx.Exec(ctx, `UPDATE server_delivery_jobs SET observed_refresh_revision=refresh_revision WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number); err != nil {
		return err
	}
	return tx.PutDeliveryObservationQuery(ctx, row)
}

func (f *historicalHostedDelivery) execute(t *testing.T, who protocol.Principal, command app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
	t.Helper()
	if target.ID == "" {
		if prefix := protocol.CreationTargetPrefix(command.Operation()); prefix != "" {
			target.ID = f.ids.New(prefix)
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if identities := protocol.NativePayloadIdentities(command.Operation()); len(identities) > 0 {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		for field, prefix := range identities {
			if len(fields[field]) == 0 || string(fields[field]) == `""` {
				fields[field], _ = json.Marshal(f.ids.New(prefix))
			}
		}
		raw, _ = json.Marshal(fields)
	}
	if command.Operation() == protocol.OpRunnerClaim {
		var p protocol.ClaimPayload
		_ = json.Unmarshal(raw, &p)
		if p.RunID == "" {
			p.RunID = f.ids.New(protocol.PrefixRun)
		}
		raw, _ = json.Marshal(p)
	}
	op, err := f.e.svc.app.Execute(t.Context(), who, &protocol.Envelope{RequestID: f.ids.New(protocol.PrefixRequest), Operation: command.Operation(), Target: target, Payload: raw, ExpectedControlRevision: revision}, command)
	if err != nil || op.Error != nil {
		t.Fatal(command.Operation(), op, err)
	}
	if command.Operation() == "issues.create" && op.Status == protocol.OpSucceeded {
		var i protocol.Issue
		if err := json.Unmarshal(op.Result, &i); err != nil {
			t.Fatal(err)
		}
		i, err = issuefixture.Retained(t.Context(), f.e.store, f.ids, f.clock.Now(), i.IssueID, "")
		if err != nil {
			t.Fatal(err)
		}
		op.Result, _ = json.Marshal(i)
	}
	return op
}

func (f *historicalHostedDelivery) currentIssue(t *testing.T) protocol.Issue {
	t.Helper()
	var value protocol.Issue
	if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		value, err = store.GetCanonicalIssue(t.Context(), tx.Q(), f.issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *historicalHostedDelivery) duty(t *testing.T) protocol.IssueDeliveryDuty {
	t.Helper()
	var value protocol.IssueDeliveryDuty
	if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		value, err = store.GetIssueDeliveryDuty(t.Context(), tx.Q(), f.issue.IssueID, f.source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *historicalHostedDelivery) count(t *testing.T, table string) int {
	t.Helper()
	var count int
	if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

// Task creation, owner approval, worker results, independent verdicts and hosted
// admission use their normal commands. Installation receipts and the already
// persisted later-maintenance invalidation are explicit external fact doubles;
// this test exercises the actual callback rather than another preflight API.
func newHistoricalHostedDelivery(t *testing.T) *historicalHostedDelivery {
	t.Helper()
	f := &historicalHostedDelivery{e: newEnv(t), clock: testkit.NewClock(), owner: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "owner"}}
	f.ids = testkit.NewIDGen(f.clock, 192)
	f.execute(t, f.owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, new(int64))
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "test-hosted", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "test-model", Effort: "high", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.execute(t, protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, new(int64))
	f.e.svc.app = app.New(f.e.store, f.ids, f.clock.Now, app.WithHostedConfiguration(prepared))
	op := f.execute(t, f.owner, app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Settle the installed delivery", Project: "default", Intent: "Retain the accepted deployment and later maintenance separately", Deliveries: []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}}}, nil)
	if err := json.Unmarshal(op.Result, &f.issue); err != nil {
		t.Fatal(err)
	}
	op = f.execute(t, f.owner, app.HostedIssueAssignment{}, protocol.Target{Type: "issue", ID: f.issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: f.issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/planner"}, Reason: "Settle this Issue's recorded deployment"}, &f.issue.Revision)
	_ = json.Unmarshal(op.Result, &f.issue)
	op = f.execute(t, f.owner, app.HostedIssueAssignment{Finish: true}, protocol.Target{Type: "issue", ID: f.issue.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: f.issue.AssignmentEpoch, HandoffID: f.issue.PendingHandoffID, Reason: "No prior manager"}, &f.issue.Revision)
	_ = json.Unmarshal(op.Result, &f.issue)
	runtime := protocol.RunnerRuntime{InstanceID: "fixture-runner", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityWorkerObservation}}
	runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}
	f.execute(t, runner, app.Hello{}, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &runtime}, nil)
	create := func(authorization *protocol.IssueDeliveryAuthorization, spec protocol.OwnerTaskSpec) string {
		t.Helper()
		issue := f.currentIssue(t)
		op := f.execute(t, f.owner, app.CanonicalOwnerTaskCreate{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "This historical deployment fixture requires a real independent passed verdict for its original frozen output"}, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Execute the recorded scope", Spec: spec, DeliveryAuthorization: authorization}, &issue.Revision)
		var result struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil || result.TaskID == "" {
			t.Fatal(op, err)
		}
		return result.TaskID
	}
	finish := func(task string, artifact protocol.ArtifactPayload) {
		t.Helper()
		claim := func(role protocol.RunRole) protocol.Assignment {
			t.Helper()
			runtime.Sequence++
			op := f.execute(t, runner, app.Claim{}, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: &runtime}, nil)
			var result struct {
				Assignment *protocol.Assignment `json:"assignment"`
			}
			if err := json.Unmarshal(op.Result, &result); err != nil || result.Assignment == nil || result.Assignment.TaskID != task || result.Assignment.Role != role {
				t.Fatal("claim", op, err)
			}
			return *result.Assignment
		}
		impl := claim(protocol.RoleImplement)
		who := protocol.Principal{Kind: protocol.PrincipalRun, Subject: impl.RunID, Role: impl.Role}
		f.execute(t, who, app.ReportRunStarted{}, protocol.Target{Type: "run", ID: impl.RunID}, nil, nil)
		if task == f.execution {
			if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), f.batch) }); err != nil {
				t.Fatal(err)
			}
		}
		if len(artifact.Mounts) > 0 {
			var taskRow store.TaskRow
			if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				taskRow, err = store.GetTask(t.Context(), tx.Q(), task)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			artifact.Mounts[0].Branch = domain.TaskBranch(taskRow.WorkspaceName)
		}
		f.execute(t, who, app.SubmitArtifact{}, protocol.Target{Type: "run", ID: impl.RunID}, artifact, nil)
		accept := claim(protocol.RoleAccept)
		f.execute(t, protocol.Principal{Kind: protocol.PrincipalRun, Subject: accept.RunID, Role: accept.Role}, app.SubmitAcceptance{}, protocol.Target{Type: "run", ID: accept.RunID}, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"verified":"original frozen output"}`)}, nil)
		f.execute(t, runner, app.ReleaseWorkspace{}, protocol.Target{Type: "workspace", ID: impl.Workspace.ID}, protocol.WorkspaceRelease{TaskID: task, WorkspaceID: impl.Workspace.ID}, nil)
	}
	f.source = create(nil, protocol.OwnerTaskSpec{Goal: "Implement the accepted source", Acceptance: []string{"Independently verify the source"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}})
	finish(f.source, protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: strings.Repeat("b", 40), CommitSHA: strings.Repeat("a", 40), Changed: true}}})
	candidate := protocol.IssueDeliveryCandidate{DeliveryID: "pr", URL: "https://example.invalid/repo/pull/17", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), MergeSHA: strings.Repeat("c", 40), TargetSHA: strings.Repeat("c", 40), ProviderState: "merged", Validation: "Independent review and checks on the approved source", Deploy: true, DeploymentScope: "Install this source on the server and retained runner", Deployment: &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}}
	f.issue = f.currentIssue(t)
	op = f.execute(t, f.owner, app.AttentionCommand{Action: "create"}, protocol.Target{Type: "attention"}, protocol.CreateAttentionPayload{IssueID: f.issue.IssueID, Kind: "decision", Title: protocol.DeliveryCandidateQuestion(candidate), Options: []string{"approve", "defer", "end_delivery"}, BlocksClose: true}, nil)
	var attention protocol.Attention
	if err := json.Unmarshal(op.Result, &attention); err != nil {
		t.Fatal(err)
	}
	duty := f.duty(t)
	duty.Candidate, duty.AttentionID, duty.AssignmentEpoch, duty.Status = &candidate, attention.AttentionID, f.issue.AssignmentEpoch, "awaiting_owner"
	duty.Revision++
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutIssueDeliveryDuty(t.Context(), f.issue.IssueID, duty); err != nil {
			return err
		}
		return f.putObservation(tx, t.Context(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: f.issue.IssueID, DeliveryID: "pr", CheckedAt: f.clock.Now().UnixMilli(), DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA, MergeSHA: candidate.MergeSHA, TargetSHA: candidate.TargetSHA, TargetContainsMerge: true, URL: candidate.URL}}})
	}); err != nil {
		t.Fatal(err)
	}
	f.execute(t, f.owner, app.AttentionCommand{Action: "respond"}, protocol.Target{Type: "attention", ID: attention.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: "approve"}, &attention.Revision)
	duty = f.duty(t)
	f.execution = create(&protocol.IssueDeliveryAuthorization{TaskID: f.source, Revision: duty.Revision}, protocol.OwnerTaskSpec{Goal: "Execute the approved installation", Acceptance: []string{"Verify installed source and original behavior"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}})
	hash := strings.Repeat("e", 64)
	f.batch = store.UpgradeRow{DeliveryTaskID: f.execution, View: protocol.UpgradeView{UpgradeID: "historical-install", ReleaseID: "original-release", SourceCommit: candidate.TargetSHA, Revision: 22, Status: "completed", Phase: "completed", CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "original-release"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", ServerProcess: "original-process", ServerHealthy: 2, Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
	finish(f.execution, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Verified original installation and behavior", Evidence: []string{"upgrade:" + f.batch.View.UpgradeID}}})
	f.original = f.duty(t)
	f.clock.Advance(time.Second)
	maintenance := f.batch
	maintenance.DeliveryTaskID, maintenance.View.UpgradeID, maintenance.View.SourceCommit, maintenance.View.Revision = "", "later-maintenance", strings.Repeat("f", 40), 24
	maintenance.View.CreatedAt, maintenance.View.UpdatedAt = f.clock.Now(), f.clock.Now()
	duty = f.original
	duty.Status, duty.Revision = "preparing", duty.Revision+1
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutUpgrade(t.Context(), maintenance); err != nil {
			return err
		}
		if err := tx.PutIssueDeliveryDuty(t.Context(), f.issue.IssueID, duty); err != nil {
			return err
		}
		return f.putObservation(tx, t.Context(), store.DeliveryObservationRow{Observation: protocol.DeliveryObservation{WorkItemID: f.issue.IssueID, DeliveryID: "pr", CheckedAt: f.clock.Now().UnixMilli(), Observer: "server", BaselineUpgradeID: maintenance.View.UpgradeID, BaselineUpgradeRevision: maintenance.View.Revision, DeliverySnapshot: protocol.DeliverySnapshot{State: "merged", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA, MergeSHA: candidate.MergeSHA, TargetSHA: maintenance.View.SourceCommit, TargetContainsMerge: true, URL: candidate.URL}}})
	}); err != nil {
		t.Fatal(err)
	}
	f.issue = f.currentIssue(t)
	conversation := "issue:" + f.issue.IssueID
	f.execute(t, f.owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Settle only the original accepted installation"}, nil)
	delivery, _, ok, err := f.e.svc.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	selection, named, err := f.e.svc.app.HostedSelectionForAdmission(t.Context(), delivery.RequestID, conversation)
	if err != nil || !named {
		t.Fatal(named, err)
	}
	var input app.HandleMessage
	if err := json.Unmarshal(delivery.Payload, &input); err != nil {
		t.Fatal(err)
	}
	f.turn = store.MainTurn{RequestID: delivery.RequestID, MessageID: input.MessageID, ThreadID: "settlement-thread", Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model, Effort: selection.Definition.Effort}
	if err := f.e.svc.app.PutMainTurn(t.Context(), f.turn); err != nil {
		t.Fatal(err)
	}
	f.turn.Status, f.turn.TurnID = store.TurnStarted, "settlement-turn"
	if err := f.e.svc.app.PutMainTurn(t.Context(), f.turn); err != nil {
		t.Fatal(err)
	}
	f.principal = protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "issue-manager", Thread: f.turn.ThreadID, ConversationID: conversation}
	return f
}

func (f *historicalHostedDelivery) callback(t *testing.T, request string, expected, epoch int64) answerView {
	t.Helper()
	payload := protocol.UpdateIssueDeliveryPayload{TaskID: f.source, Action: "complete", UpgradeID: f.batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: epoch, Reason: "Settle the original independently accepted installation"}}
	body, _ := json.Marshal(payload)
	args, _ := json.Marshal(hostedMutationArgs{EffectSeq: 1, ExpectedControlRevision: &expected, Payload: body})
	result := f.e.svc.Handle(t.Context(), Call{Principal: f.principal, Native: "historical-settlement", RequestID: request, TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "issues_update_delivery", Arguments: args})
	var answer answerView
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || answer.OK != result.Success {
		t.Fatal(result, err)
	}
	return answer
}

func TestHistoricalDeliveryHostedCallbackSettlesOriginalDeployment(t *testing.T) {
	f := newHistoricalHostedDelivery(t)
	issue := f.currentIssue(t)
	request := f.ids.New(protocol.PrefixRequest)
	tasks, upgrades, actions := f.e.tasks(), f.count(t, "upgrades"), f.count(t, "plan_actions")
	first := f.callback(t, request, issue.Revision, issue.AssignmentEpoch)
	if !first.OK {
		t.Fatal(first.Error)
	}
	again := f.callback(t, request, issue.Revision, issue.AssignmentEpoch)
	if again.OK || again.Error == nil || again.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatal("original callback did not resolve its completed business effect", first, again)
	}
	duty := f.duty(t)
	if duty.Status != "complete" || duty.ExecutionTaskID != f.execution || !reflect.DeepEqual(duty.Candidate, f.original.Candidate) || !reflect.DeepEqual(duty.Authorization, f.original.Authorization) {
		t.Fatal(duty)
	}
	if f.e.tasks() != tasks || f.count(t, "upgrades") != upgrades || f.count(t, "plan_actions") != actions || f.count(t, "local_hosted_coordination_effects") != 1 {
		t.Fatal("settlement scheduled or installed new work")
	}
	var batch store.UpgradeRow
	if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		batch, err = store.GetUpgrade(t.Context(), tx.Q(), f.batch.View.UpgradeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch, f.batch) {
		t.Fatal("settlement rewrote original installation", batch)
	}
}

func TestHistoricalDeliveryHostedFailurePreservesOriginalReceipt(t *testing.T) {
	for _, kind := range []string{"stale_cas", "stale_epoch", "unknown_installation"} {
		t.Run(kind, func(t *testing.T) {
			f := newHistoricalHostedDelivery(t)
			issue := f.currentIssue(t)
			version, epoch := issue.Revision, issue.AssignmentEpoch
			if kind == "stale_cas" {
				version--
			}
			if kind == "stale_epoch" {
				epoch--
			}
			if kind == "unknown_installation" {
				batch := f.batch
				batch.View.Status, batch.View.Phase, batch.View.Reason = "attention", "checking", "installation outcome remains unknown"
				if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
					t.Fatal(err)
				}
			}
			request := f.ids.New(protocol.PrefixRequest)
			before := f.duty(t)
			first := f.callback(t, request, version, epoch)
			if first.OK || first.Error == nil {
				t.Fatal(first)
			}
			var result struct {
				RequestID string            `json:"request_id"`
				Status    protocol.OpStatus `json:"status"`
			}
			if err := json.Unmarshal(first.Result, &result); err != nil || result.RequestID != request || result.Status != protocol.OpFailed {
				t.Fatal("failed callback hid its original request", first, err)
			}
			again := f.callback(t, request, version, epoch)
			if !reflect.DeepEqual(first, again) || !reflect.DeepEqual(before, f.duty(t)) || f.count(t, "local_hosted_coordination_effects") != 0 {
				t.Fatal("failed original request created an effect or lost replay", first, again)
			}
		})
	}
}

func TestHistoricalDeliverySettlementDoesNotRenewExecutionAuthority(t *testing.T) {
	f := newHistoricalHostedDelivery(t)
	if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		authority, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), f.execution)
		if err != nil {
			return err
		}
		if authority.Valid || authority.Settlement == nil || !authority.Settlement.Eligible || authority.Settlement.UpgradeID != f.batch.View.UpgradeID {
			t.Fatal("historical accounting authority became new execution authority", authority)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	issue := f.currentIssue(t)
	payload, _ := json.Marshal(protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Do not repeat the historic installation", Spec: protocol.OwnerTaskSpec{Goal: "Repeat the old source", Acceptance: []string{"Verify the result"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}, DeliveryAuthorization: &protocol.IssueDeliveryAuthorization{TaskID: f.source, Revision: f.original.Revision}})
	args, _ := json.Marshal(hostedMutationArgs{EffectSeq: 1, ExpectedControlRevision: &issue.Revision, Payload: payload})
	call := Call{Principal: f.principal, Native: "new-execution-from-old-grant", RequestID: f.ids.New(protocol.PrefixRequest), TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "issues_create_task", Arguments: args}
	tasks, upgrades, actions := f.e.tasks(), f.count(t, "upgrades"), f.count(t, "plan_actions")
	result := f.e.svc.Handle(t.Context(), call)
	var answer answerView
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("old settlement grant admitted another execution", result, err)
	}
	probe := &upgradeStarterProbe{}
	f.e.svc.SetUpgradeStarter(probe)
	call.Tool, call.Native, call.RequestID = "upgrades_start", "new-install-from-old-grant", f.ids.New(protocol.PrefixRequest)
	call.Arguments, _ = json.Marshal(protocol.UpgradeStartRequest{RequestID: call.RequestID, UpgradeID: "must-not-install", DeliveryTaskID: f.execution, Release: "/verified/test-bundle"})
	result = f.e.svc.Handle(t.Context(), call)
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeForbidden || len(probe.inputs) != 0 {
		t.Fatal("Issue settlement authority reached the installer", result, err, probe.inputs)
	}
	if f.e.tasks() != tasks || f.count(t, "upgrades") != upgrades || f.count(t, "plan_actions") != actions || f.count(t, "local_hosted_coordination_effects") != 0 {
		t.Fatal("rejected new mutations left a business effect")
	}
}
