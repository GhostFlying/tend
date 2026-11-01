package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type namedWorkspaces struct{ *workspaces }

func (w namedWorkspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	p, err := w.workspaces.Prepare(ctx, as)
	p.Report.BackendID = as.Workspace.BackendID
	return p, err
}

// This fake models an app-server's per-run session closure, without a TUI releaser.
type namedSessionWorkers struct{ workers *testkit.Workers }

func (w namedSessionWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	return w.workers.Start(ctx, spec)
}

func TestEndToEnd_NamedExecutionReloadRestartAndIndependentAcceptance(t *testing.T) {
	for _, kind := range []agentconfig.RuntimeKind{agentconfig.AppServerWorker, agentconfig.OrcaWorker} {
		t.Run(string(kind), func(t *testing.T) { testNamedExecutionReloadRestart(t, kind, "") })
	}
}
func TestEndToEnd_NamedReusedWorkspaceFirstRunRequiresBothRoles(t *testing.T) {
	for _, mode := range []string{"reuse", "default"} {
		t.Run(mode, func(t *testing.T) { testNamedExecutionReloadRestart(t, agentconfig.AppServerWorker, mode) })
	}
}
func testNamedExecutionReloadRestart(t *testing.T, reviewKind agentconfig.RuntimeKind, reuseMode string) {
	s := newServer(t, time.Minute)
	f := newInventoryProcess(t, s)
	reviewOwner := newOwnerCLI(t, filepath.Join(t.TempDir(), "owner"))
	reviewOwner.run("", "login", "--server", s.startAPI())
	reviewOwner.run("", "category", "put", "reviewer", "--title", "Reviewer")
	reviewRuntime := f.config.Runtimes[0]
	reviewRuntime.ID = "review-runtime"
	reviewRuntime.Kind = reviewKind
	if reviewKind == agentconfig.OrcaWorker {
		reviewRuntime.CLI = "codex"
	}
	f.config.Runtimes = append(f.config.Runtimes, reviewRuntime)
	f.config.Agents[0].Model = "implement-old"
	f.config.Agents[0].Instructions = "IMPLEMENT_PRIVATE_ROLE"
	f.config.Agents = append(f.config.Agents, agentconfig.AgentDefinition{LocalID: "review", Revision: 1, DisplayName: "Review", RuntimeID: "review-runtime", Categories: []string{"reviewer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "review-old", Effort: "xhigh", Instructions: "REVIEW_PRIVATE_ROLE", Enabled: true})
	install := f.options.Config.Installations[0]
	install.RuntimeID = "review-runtime"
	f.options.Config.Installations = append(f.options.Config.Installations, install)
	f.write()
	gate := make(chan struct{})
	var mu sync.Mutex
	var starts []harness.WorkerSpec
	var reviewFactories []*testkit.Workers
	var currentPool *agentexec.Pool
	backend := "git"
	if reviewKind == agentconfig.OrcaWorker {
		backend = "orca"
	}
	start := func(initial bool) {
		t.Helper()
		source, err := agentcontrol.Open(t.Context(), f.options)
		if err != nil {
			t.Fatal(err)
		}
		f.control = source
		if initial {
			if _, err := source.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: s.ids.New(protocol.PrefixRequest)}); err != nil {
				t.Fatal(err)
			}
		}
		workspaces := &workspaces{dir: filepath.Join(f.home, "workspaces")}
		pool, err := agentexec.New(agentexec.Options{Source: source, BackendID: backend, Workspaces: namedWorkspaces{workspaces}, Probe: func(context.Context, agentexec.BuildInput) error { return nil }, Build: func(input agentexec.BuildInput) (harness.Workers, error) {
			workers := &testkit.Workers{Script: func(spec harness.WorkerSpec) testkit.WorkerScript {
				mu.Lock()
				starts = append(starts, spec)
				mu.Unlock()
				result := harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "implemented"}}}
				script := testkit.WorkerScript{Result: result}
				if spec.Role == protocol.RoleImplement {
					script.Gate = gate
				} else {
					script.Result = harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: protocol.VerdictPassed}}
				}
				return script
			}}
			if input.Runtime.Kind == agentconfig.OrcaWorker {
				mu.Lock()
				reviewFactories = append(reviewFactories, workers)
				mu.Unlock()
				return workers, nil
			}
			return namedSessionWorkers{workers: workers}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		currentPool = pool
		j, err := journal.Open(t.Context(), filepath.Join(f.home, "journal.db"), s.now)
		if err != nil {
			t.Fatal(err)
		}
		r := runner.New(runner.Config{ServerURL: s.url, HostID: "linux-1", HostToken: hostToken, Journal: j, Workers: namedSessionWorkers{workers: &testkit.Workers{Script: passing}}, Workspaces: workspaces, Executions: pool, AgentInventory: source.Status, AgentChanges: source.Changes(), IDs: protocol.NewULIDGen(s.now, nil), Now: s.now, Log: quiet(), RetryDelay: 10 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond})
		f.runner = r
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		f.done = make(chan error, 1)
		go func() { err := r.Run(ctx); _ = j.Close(); _ = source.Close(); f.done <- err }()
	}
	start(true)
	eventually(t, "named adapter observations registered", func() bool {
		status, err := f.runner.AgentRegistrationStatus(t.Context())
		return err == nil && status.Server != nil && len(status.Server.Executors) == 2 && status.Server.Executors[0].Available && status.Server.Executors[1].Available
	})
	f.stop()
	claimQueued := func() *protocol.Assignment {
		t.Helper()
		var runtime store.HostRuntimeRow
		if err := s.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			runtime, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		op := s.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: &runtime.Runtime}, app.Claim{})
		var result struct {
			Assignment *protocol.Assignment `json:"assignment"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result.Assignment
	}
	issueRaw := reviewOwner.run("", "issue", "create", "--data", `{"plan":{"title":"Named work","project":"default","intent":"Implement and independently verify the requested change","stages":[]}}`)
	var issueResponse protocol.APIResponse
	if err := json.Unmarshal([]byte(issueRaw), &issueResponse); err != nil || issueResponse.Operation == nil {
		t.Fatal(issueRaw, err)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(issueResponse.Operation.Result, &issue); err != nil {
		t.Fatal(err)
	}
	spec := protocol.OwnerTaskSpec{Goal: "named work", Acceptance: []string{"independent check"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}, ImplementationContext: "AUTHOR_RATIONALE_PRIVATE", AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", RuntimeID: "worker", Model: "not-selected-model", Effort: "high"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer", RuntimeID: "review-runtime", Effort: "xhigh"}}}
	payload, err := json.Marshal(protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "named work", Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	taskRaw := reviewOwner.run("", "issue", "create-task", issue.IssueID, "--data", string(payload))
	var taskResponse protocol.APIResponse
	if err := json.Unmarshal([]byte(taskRaw), &taskResponse); err != nil || taskResponse.Operation == nil {
		t.Fatal(taskRaw, err)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(taskResponse.Operation.Result, &created); err != nil {
		t.Fatal(err)
	}
	if assignment := claimQueued(); assignment != nil {
		t.Fatal("owner implementation model constraint was ignored", assignment.Execution)
	}
	spec.AgentSelection.Implementation.Model = "implement-old"
	spec.AgentSelection.Implementation.Effort = "low"
	update, err := json.Marshal(protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec})
	if err != nil {
		t.Fatal(err)
	}
	reviewOwner.run("", "issue", "update-task", created.TaskID, "--data", string(update))
	if assignment := claimQueued(); assignment != nil {
		t.Fatal("owner implementation effort constraint was ignored", assignment.Execution)
	}
	spec.AgentSelection.Implementation.Effort = "high"
	update, err = json.Marshal(protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec})
	if err != nil {
		t.Fatal(err)
	}
	reviewOwner.run("", "issue", "update-task", created.TaskID, "--data", string(update))
	start(false)

	eventually(t, "implementation started", func() bool { mu.Lock(); defer mu.Unlock(); return len(starts) == 1 })
	mu.Lock()
	first := starts[0]
	mu.Unlock()
	if first.FrozenExecution == nil || first.FrozenExecution.Model != "implement-old" || first.FrozenExecution.Effort != "high" || first.RoleInstructions != "IMPLEMENT_PRIVATE_ROLE" {
		t.Fatal("implementation ignored named definition", first)
	}
	withoutSelection := spec
	withoutSelection.AgentSelection = nil
	rejected, err := json.Marshal(protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &withoutSelection})
	if err != nil {
		t.Fatal(err)
	}
	reviewOwner.out.Reset()
	code := cli.Main(t.Context(), reviewOwner.env, []string{"issue", "update-task", created.TaskID, "--data", string(rejected)})
	var rejection protocol.APIResponse
	err = json.Unmarshal(reviewOwner.out.Bytes(), &rejection)
	if code != 1 || err != nil || rejection.Operation == nil || rejection.Operation.Error == nil || rejection.Operation.Error.Code != protocol.CodeInvalidRequest || s.task(created.TaskID).SpecVersion != 3 {
		t.Fatal("owner removal of selection changed a named workspace", code, rejection, err)
	}
	f.config.Agents[0].Revision++
	f.config.Agents[0].Model = "implement-new"
	f.config.Agents[0].Instructions = "IMPLEMENT_CHANGED_ROLE"
	f.config.Agents[1].Revision++
	f.config.Agents[1].Model = "review-new"
	f.config.Agents[1].Instructions = "REVIEW_CHANGED_ROLE"
	f.write()
	if _, err := f.control.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: s.ids.New(protocol.PrefixRequest), ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "new inventory registered", func() bool { return f.current() != nil && f.current().LocalRevision == 2 })
	f.stop()
	start(false)
	eventually(t, "same run resumed through old snapshot", func() bool { mu.Lock(); defer mu.Unlock(); return len(starts) >= 2 })
	mu.Lock()
	resumed := starts[1]
	mu.Unlock()
	if resumed.RunID != first.RunID || resumed.Attempt != 2 || resumed.FrozenExecution.Model != "implement-old" || resumed.RoleInstructions != "IMPLEMENT_PRIVATE_ROLE" || resumed.Thread == "" {
		t.Fatalf("restart changed original run configuration: first=%+v resumed=%+v", first, resumed)
	}
	close(gate)
	eventually(t, "independent acceptance completed", func() bool { return s.task(created.TaskID).Lifecycle == protocol.TaskCompleted })
	mu.Lock()
	observedStarts := append([]harness.WorkerSpec(nil), starts...)
	observedFactories := append([]*testkit.Workers(nil), reviewFactories...)
	mu.Unlock()
	if len(observedStarts) != 3 {
		t.Fatalf("starts=%d", len(observedStarts))
	}
	review := observedStarts[2]
	if review.Role != protocol.RoleAccept || review.RunID == first.RunID || review.Thread != "" || review.FrozenExecution.Model != "review-new" || review.RoleInstructions != "REVIEW_CHANGED_ROLE" || strings.Contains(string(review.Spec), "AUTHOR_RATIONALE_PRIVATE") {
		t.Fatalf("acceptance inherited implementation or missed its new run selection: %+v", review)
	}
	if reviewKind == agentconfig.OrcaWorker {
		releases := 0
		for _, factory := range observedFactories {
			releases += len(factory.Released())
		}
		if releases == 0 {
			t.Fatal("mixed runtime acceptance did not stop through its original Orca factory")
		}
	}

	if reuseMode == "" {
		return
	}
	// The fake native sessions have finished. Report that stopped resource fact
	// through the actual runner endpoint before asking for a new task to reuse it.
	var sourceWorkspace string
	if err := s.store.Read(t.Context(), func(tx *store.ReadTx) error {
		w, err := store.GetWorkspaceByTask(t.Context(), tx.Q(), created.TaskID)
		sourceWorkspace = w.WorkspaceID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	release := s.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpWorkspaceRelease, protocol.Target{Type: "workspace", ID: sourceWorkspace}, protocol.WorkspaceRelease{WorkspaceID: sourceWorkspace, TaskID: created.TaskID}, app.ReleaseWorkspace{})
	var released protocol.WorkspaceReleaseResult
	if err := json.Unmarshal(release.Result, &released); err != nil || !released.Released {
		t.Fatal("finished fake sessions did not release resource", released, err)
	}

	observed, err := currentPool.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, executor := range observed {
		if executor.Pin.Identity.LocalID == "review" {
			currentPool.Reject(executor.Pin, "test_adapter_unavailable")
		}
	}
	eventually(t, "review adapter unavailable", func() bool {
		status, err := f.runner.AgentRegistrationStatus(t.Context())
		if err != nil || status.Server == nil {
			return false
		}
		for _, executor := range status.Server.Executors {
			if executor.Pin.Identity.LocalID == "review" {
				return !executor.Available && executor.Reason == "test_adapter_unavailable"
			}
		}
		return false
	})
	f.stop()
	var runtime store.HostRuntimeRow
	if err := s.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		runtime, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var currentIssue protocol.IssueView
	if err := json.Unmarshal([]byte(reviewOwner.run("", "issue", "show", issue.IssueID, "--json")), &currentIssue); err != nil {
		t.Fatal(err)
	}
	followSpec := spec
	selection := *spec.AgentSelection
	selection.Implementation.Model = ""
	selection.Implementation.Effort = ""
	followSpec.AgentSelection = &selection
	followSpec.Workspace = protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceReuse, SourceTaskID: created.TaskID}
	if reuseMode == "default" {
		plan := currentIssue.Plan
		plan.DefaultWorkspaceTaskID = created.TaskID
		changed, err := json.Marshal(protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: currentIssue.AssignmentEpoch, Plan: plan})
		if err != nil {
			t.Fatal(err)
		}
		reviewOwner.run("", "issue", "update", issue.IssueID, "--data", string(changed))
		followSpec.Workspace = protocol.OwnerTaskWorkspace{}
	}
	createFollow, err := json.Marshal(protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: currentIssue.AssignmentEpoch, Title: "followup uses retained workspace", Spec: followSpec, UseDefaultWorkspace: reuseMode == "default"})
	if err != nil {
		t.Fatal(err)
	}
	rawFollow := reviewOwner.run("", "issue", "create-task", issue.IssueID, "--data", string(createFollow))
	var followResponse protocol.APIResponse
	if err := json.Unmarshal([]byte(rawFollow), &followResponse); err != nil || followResponse.Operation == nil {
		t.Fatal(rawFollow, err)
	}
	var follow struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(followResponse.Operation.Result, &follow); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Read(t.Context(), func(tx *store.ReadTx) error {
		w, err := store.GetWorkspaceByTask(t.Context(), tx.Q(), follow.TaskID)
		if err != nil {
			return err
		}
		var runs int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM runs WHERE task_id=?`, follow.TaskID).Scan(&runs); err != nil {
			return err
		}
		if w.HostID != "linux-1" || runs != 0 {
			t.Fatal("fixture is not a new task on a retained workspace", w, runs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claim := s.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: &runtime.Runtime}, app.Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claim.Result, &result); err != nil || result.Assignment != nil {
		t.Fatal("new reused task claimed without an eligible reviewer", result.Assignment, err)
	}
	// New process probes the same accepted definitions again and restores both
	// adapters. The new task may now allocate, without changing the source task's pin.
	start(false)
	eventually(t, "reused task executes after both roles are available", func() bool { return s.task(follow.TaskID).Lifecycle == protocol.TaskCompleted })

}
