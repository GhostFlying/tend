package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func runner(host string) protocol.Principal {
	return protocol.Principal{Kind: protocol.PrincipalRunner, Subject: host}
}

// claim sends a claim with a fresh request ID and returns the assignment,
// if any, together with the envelope for replays.
func (h *harness) claim(host string) (protocol.Assignment, bool, *protocol.Envelope) {
	h.t.Helper()
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: host}, map[string]any{}, nil)
	as, ok := h.claimWith(host, env)
	return as, ok, env
}

func (h *harness) claimWith(host string, env *protocol.Envelope) (protocol.Assignment, bool) {
	h.t.Helper()
	op := h.exec(runner(host), env, Claim{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("claim: %+v", op)
	}
	var res struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &res); err != nil {
		h.t.Fatal(err)
	}
	if res.Assignment == nil {
		return protocol.Assignment{}, false
	}
	return *res.Assignment, true
}

func (h *harness) hello(host string, held ...protocol.HeldRun) protocol.HelloResult {
	h.t.Helper()
	op := h.exec(runner(host), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: host}, protocol.HelloPayload{Held: held}, nil), Hello{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("hello: %+v", op)
	}
	var res protocol.HelloResult
	if err := json.Unmarshal(op.Result, &res); err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) runAs(as protocol.Assignment, op string, payload any, cmd Command) protocol.Operation {
	h.t.Helper()
	return h.exec(runPrincipal(as.RunID, as.Role), h.env(op, runTarget(as.RunID), payload, nil), cmd)
}

func (h *harness) run(id string) store.RunRow {
	h.t.Helper()
	var r store.RunRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		r, err = store.GetRun(context.Background(), tx.Q(), id)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) lease(runID string) store.LeaseRow {
	h.t.Helper()
	var l store.LeaseRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		l, err = store.GetLeaseByRun(context.Background(), tx.Q(), runID)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return l
}

// withMac adds a second host serving the same repositories.
func (h *harness) withMac() {
	c := testConfig()
	c.Hosts = append(c.Hosts, HostConfig{HostID: "mac-1", OS: "darwin", Arch: "arm64", Repos: []string{"tend", "douyin", "template-assembler"},
		Slots: 2, TokenHash: strings.Repeat("cd", 32), Nodes: []string{"tag:tend-runner"}})
	h.syncConfig(c)
}

func TestClaimAllocatesRunLeaseAndWorkspace(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec, WorkspaceName: "feature_a"})

	as, ok, env := h.claim("linux-1")
	if !ok {
		t.Fatal("no assignment for a queued task")
	}
	if as.TaskID != res.TaskID || as.Role != protocol.RoleImplement || as.Generation != 1 || as.LeaseTTLMillis != DefaultLeaseTTL.Milliseconds() {
		t.Errorf("assignment = %+v", as)
	}
	ws := as.Workspace
	if ws.Name != "feature_a" || ws.Branch != "tend/feature_a" || len(ws.Mounts) != 1 || ws.Mounts[0].RemoteURL != "https://example.com/tend.git" {
		t.Errorf("workspace = %+v", ws)
	}
	if as.RunToken != "" {
		t.Error("the ledger result must not carry a run token")
	}
	if l := h.lease(as.RunID); l.LeaseID != as.LeaseID || l.Status != store.LeaseActive || l.HostID != "linux-1" {
		t.Errorf("lease = %+v", l)
	}
	if !h.task(res.TaskID).WorkspaceAssigned {
		t.Error("claim did not create the workspace")
	}

	// A retried claim returns the same assignment.
	if again, ok := h.claimWith("linux-1", env); !ok || again.RunID != as.RunID {
		t.Errorf("retried claim = %+v, want run %s", again, as.RunID)
	}
	// An idle poll finds nothing and leaves no request record.
	before := h.count("request_records")
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Error("second claim found work")
	}
	if n := h.count("request_records"); n != before {
		t.Errorf("idle claim recorded a request: %d -> %d", before, n)
	}
	if n := h.count("runs"); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
}

func TestFault_ClaimReceiptLost(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	// The claim committed, but the runner never journaled it.
	lost, _, _ := h.claim("linux-1")

	res := h.hello("linux-1")
	if len(res.Reassigned) != 1 || res.Reassigned[0].RunID != lost.RunID || res.Reassigned[0].LeaseID != lost.LeaseID {
		t.Fatalf("hello reassigned %+v, want run %s", res.Reassigned, lost.RunID)
	}
	if n := h.count("runs"); n != 1 {
		t.Errorf("runs = %d, want 1: the same run is handed over again", n)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Error("a second run was claimed for the same work")
	}
}

func TestFault_HeartbeatLostRunnerAlive(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})

	// Heartbeats stop arriving; the lease runs out.
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(context.Background()); err != nil || n != 1 {
		t.Fatalf("ExpireLeases = %d, %v", n, err)
	}
	if r := h.run(as.RunID); r.ObservedState != protocol.RunUnknown {
		t.Errorf("run = %s, want unknown", r.ObservedState)
	}
	hb, err := h.app.Heartbeat(context.Background(), runner("linux-1"), []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}})
	if err != nil || len(hb.Runs) != 1 || hb.Runs[0].Renewed {
		t.Fatalf("heartbeat on a lapsed lease = %+v, %v; want not renewed", hb, err)
	}

	// New work for the task must not get the workspace while the old run
	// may still be writing to it.
	changed := testSpec
	changed.Constraints = []string{"also fix macOS"}
	h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(res.ControlRevision)), UpdateIntent{})
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("claimed new implement work while an unknown run holds the workspace")
	}

	// The runner reconnects: the run continues, but tend now wants it to
	// stop because the spec moved on.
	hello := h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning})
	if len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictStop {
		t.Fatalf("hello verdict = %+v, want stop for an outdated run", hello.Runs)
	}
	h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stop verdict"}, Release{})
	if r := h.run(as.RunID); r.ObservedState != protocol.RunFailed {
		t.Errorf("released run = %s, want failed", r.ObservedState)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.SpecVersion != 2 {
		t.Errorf("after release: claim = %+v, %v; want the spec 2 implement run", next, ok)
	}
}

func TestHelloResumesLapsedRun(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}

	res := h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning})
	if len(res.Runs) != 1 || res.Runs[0].Verdict != protocol.VerdictContinue || res.Runs[0].Desired != protocol.DesiredRun {
		t.Fatalf("hello = %+v, want continue", res.Runs)
	}
	if r, l := h.run(as.RunID), h.lease(as.RunID); r.ObservedState != protocol.RunRunning || l.Status != store.LeaseActive {
		t.Errorf("after reconcile: run %s, lease %s", r.ObservedState, l.Status)
	}
	hb, err := h.app.Heartbeat(context.Background(), runner("linux-1"), []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}})
	if err != nil || !hb.Runs[0].Renewed {
		t.Errorf("heartbeat after reconcile = %+v, %v", hb, err)
	}
}

func TestFault_RestoreFromOldBackup(t *testing.T) {
	h := newHarness(t)
	ghost := h.ids.New(protocol.PrefixRun)
	res := h.hello("linux-1", protocol.HeldRun{RunID: ghost, LeaseID: h.ids.New(protocol.PrefixLease), Observed: protocol.RunRunning})
	if len(res.Runs) != 1 || res.Runs[0].Verdict != protocol.VerdictStop {
		t.Fatalf("hello = %+v, want stop for a run tend does not know", res.Runs)
	}
	if n := h.count("runs"); n != 0 {
		t.Errorf("runs = %d: an unknown run must not be recreated", n)
	}
}

func TestHelloMarksStartedRunLostWithoutJournal(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})

	res := h.hello("linux-1")
	if len(res.Reassigned) != 0 {
		t.Fatalf("a started run was reassigned: %+v", res.Reassigned)
	}
	if r, l := h.run(as.RunID), h.lease(as.RunID); r.ObservedState != protocol.RunUnknown || l.Status != store.LeaseLapsed {
		t.Errorf("run %s, lease %s; want unknown and lapsed", r.ObservedState, l.Status)
	}
}

func TestAcceptanceStaysOnWorkspaceHost(t *testing.T) {
	h := newHarness(t)
	h.withMac()
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit artifact: %+v", op.Error)
	}
	if r := h.run(impl.RunID); r.ObservedState != protocol.RunFinished || h.lease(impl.RunID).Status != store.LeaseReleased {
		t.Errorf("implement run after artifact: %s", r.ObservedState)
	}

	if _, ok, _ := h.claim("mac-1"); ok {
		t.Fatal("another host claimed the acceptance of an artifact it does not have")
	}
	acc, ok, _ := h.claim("linux-1")
	if !ok || acc.Role != protocol.RoleAccept {
		t.Fatalf("acceptance claim = %+v, %v", acc, ok)
	}
	var ref protocol.ArtifactPayload
	if err := json.Unmarshal(acc.Artifact, &ref); err != nil || len(ref.Mounts) != 1 || ref.Mounts[0].CommitSHA != commitSHA {
		t.Errorf("acceptance artifact = %s (%v)", acc.Artifact, err)
	}
}

// TestFault_WorkspaceHostAffinity: a later generation waits for the host
// holding the workspace, even while another host serving the same
// repositories is idle. Acceptance is covered by
// TestAcceptanceStaysOnWorkspaceHost.
func TestFault_WorkspaceHostAffinity(t *testing.T) {
	h := newHarness(t)
	h.withMac()
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(impl, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_crashed"}, Release{})
	if op := h.requestAction(res.TaskID, protocol.ActionImplement, 2); op.Status != protocol.OpSucceeded {
		t.Fatalf("retry implementation: %+v", op.Error)
	}

	// linux-1 is offline and does not claim.
	if as, ok, _ := h.claim("mac-1"); ok {
		t.Fatalf("mac-1 claimed %s generation %d away from its workspace", as.Role, as.Generation)
	}
	again, ok, _ := h.claim("linux-1")
	if !ok || again.Role != protocol.RoleImplement || again.Generation != 2 {
		t.Fatalf("claim after linux-1 returned = %+v, %v", again, ok)
	}
	if again.Workspace.Name != impl.Workspace.Name {
		t.Errorf("second generation workspace = %s, want %s", again.Workspace.Name, impl.Workspace.Name)
	}
}

// TestFault_WorkspaceExclusive: implementation and acceptance share the
// task workspace (D41), so new work waits until no run of the task may be
// using it.
func TestFault_WorkspaceExclusive(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit artifact: %+v", op.Error)
	}
	acc, ok, _ := h.claim("linux-1")
	if !ok || acc.Role != protocol.RoleAccept {
		t.Fatalf("acceptance claim = %+v, %v", acc, ok)
	}
	h.runAs(acc, protocol.OpRunsReportStarted, nil, ReportRunStarted{})

	// The spec changes while acceptance runs: a new implementation is due,
	// but the acceptance run is still in the workspace.
	changed := testSpec
	changed.Constraints = []string{"also fix macOS"}
	h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(h.task(res.TaskID).ControlRevision)), UpdateIntent{})
	if as, ok, _ := h.claim("linux-1"); ok {
		t.Fatalf("claimed %s generation %d while the acceptance run may be using the workspace", as.Role, as.Generation)
	}
	h.runAs(acc, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"}, Release{})
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Role != protocol.RoleImplement || next.SpecVersion != 2 {
		t.Errorf("claim after the acceptance run ended = %+v, %v", next, ok)
	}
}

func TestSubmitArtifactChecksMounts(t *testing.T) {
	h := newHarness(t)
	spec := testSpec
	spec.Workspace.Mounts = append(spec.Workspace.Mounts, spec.Workspace.Mounts[0])
	spec.Workspace.Mounts[1].Repo = "douyin"
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: spec})
	as, _, _ := h.claim("linux-1")
	name := h.task(res.TaskID).WorkspaceName
	only := testArtifact(name)
	unchanged := testArtifact(name)
	unchanged.Mounts = append(unchanged.Mounts, protocol.ArtifactMount{Repo: "douyin", Path: "douyin", BaseSHA: baseSHA, Branch: "tend/" + name, CommitSHA: baseSHA})
	unchanged.Mounts[0].Changed = false
	for name, p := range map[string]protocol.ArtifactPayload{"missing mount": only, "nothing changed": unchanged} {
		if op := h.runAs(as, protocol.OpRunsSubmitArtifact, p, SubmitArtifact{}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
			t.Errorf("%s: %+v", name, op)
		}
	}
	unchanged.Mounts[0].Changed = true
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, unchanged, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Errorf("two-mount artifact with one change: %+v", op.Error)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, unchanged, SubmitArtifact{}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
		t.Errorf("second artifact from the same run: %+v", op)
	}
}

func TestRunEventsAreDeduplicated(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	batch := []protocol.RunEvent{
		{Seq: 1, Kind: protocol.EventRunWaiting},
		{Seq: 2, NativeID: "item_1", Kind: "item.completed", Body: json.RawMessage(`{"text":"done"}`)},
	}
	p := runPrincipal(as.RunID, as.Role)
	for i, want := range []int{2, 0} {
		res, err := h.app.RecordRunEvents(context.Background(), p, as.RunID, batch)
		if err != nil || res.Accepted != want {
			t.Fatalf("upload %d: %+v, %v; want %d accepted", i, res, err, want)
		}
	}
	if n := h.count("run_logs"); n != 2 {
		t.Errorf("run logs = %d, want 2", n)
	}
	if r := h.run(as.RunID); r.ObservedState != protocol.RunWaiting {
		t.Errorf("run = %s, want waiting", r.ObservedState)
	}
	if _, err := h.app.RecordRunEvents(context.Background(), runPrincipal("run_other", as.Role), as.RunID, batch); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("events for another run: %v", err)
	}
}

func TestReportWorkspace(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	branch := "tend/" + h.task(res.TaskID).WorkspaceName
	report := protocol.WorkspaceReport{Mounts: []protocol.ResolvedMount{{Path: "tend", ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: branch}}}
	if op := h.runAs(as, protocol.OpRunsReportWorkspace, report, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("report: %+v", op.Error)
	}
	// The same facts can be reported again; different ones cannot.
	if op := h.runAs(as, protocol.OpRunsReportWorkspace, report, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Errorf("repeated report: %+v", op.Error)
	}
	report.Mounts[0].ResolvedBaseSHA = commitSHA
	if op := h.runAs(as, protocol.OpRunsReportWorkspace, report, ReportWorkspace{}); op.Status != protocol.OpFailed {
		t.Errorf("conflicting report: %+v", op)
	}
}

func TestHeartbeatDesiredState(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	beat := func() protocol.LeaseState {
		hb, err := h.app.Heartbeat(context.Background(), runner("linux-1"), []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}})
		if err != nil {
			t.Fatal(err)
		}
		return hb.Runs[0]
	}
	if s := beat(); !s.Renewed || s.Desired != protocol.DesiredRun {
		t.Errorf("managed: %+v", s)
	}
	h.exec(owner, h.env("control.set_mode", taskTarget(res.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(res.ControlRevision)), SetControlMode{})
	if s := beat(); s.Desired != protocol.DesiredPaused {
		t.Errorf("paused task: %+v", s)
	}
	h.exec(owner, h.env("tasks.cancel", taskTarget(res.TaskID), map[string]any{}, rev(res.ControlRevision+1)), CancelTask{})
	if s := beat(); s.Desired != protocol.DesiredStopped {
		t.Errorf("cancelled task: %+v", s)
	}
	if _, err := h.app.Heartbeat(context.Background(), runner("mac-1"), nil); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("heartbeat from an unknown host: %v", err)
	}
}

func TestArtifactWithoutStartReportAdvancesTask(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	// A state event alone does not leave starting; the start report does.
	if _, err := h.app.RecordRunEvents(context.Background(), runPrincipal(as.RunID, as.Role), as.RunID,
		[]protocol.RunEvent{{Seq: 1, Kind: protocol.EventRunRunning}}); err != nil {
		t.Fatal(err)
	}
	if r := h.run(as.RunID); r.ObservedState != protocol.RunStarting {
		t.Errorf("run after a running event = %s, want starting", r.ObservedState)
	}
	// The runner submits without a recorded start report.
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit: %+v", op.Error)
	}
	if got := h.task(res.TaskID); got.Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Errorf("task = %s, want awaiting_acceptance", got.Lifecycle)
	}
	if acc, ok, _ := h.claim("linux-1"); !ok || acc.Role != protocol.RoleAccept {
		t.Errorf("no acceptance after the artifact: %+v", acc)
	}
}

func TestHelloKeepsResultDeliverable(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The worker finished while the runner was offline.
	hello := h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunFinished})
	if hello.Runs[0].Verdict != protocol.VerdictContinue {
		t.Fatalf("hello = %+v", hello.Runs)
	}
	if r := h.run(as.RunID); r.ObservedState != protocol.RunUnknown {
		t.Errorf("run = %s, want unknown until the result arrives", r.ObservedState)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit after reconnect: %+v", op.Error)
	}
	if got := h.task(res.TaskID); got.Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Errorf("task = %s, want awaiting_acceptance", got.Lifecycle)
	}
}

func TestHelloDoesNotReviveUnwantedRun(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	lost, _, _ := h.claim("linux-1")
	h.exec(owner, h.env("tasks.cancel", taskTarget(res.TaskID), map[string]any{}, rev(res.ControlRevision)), CancelTask{})

	hello := h.hello("linux-1")
	if len(hello.Reassigned) != 0 {
		t.Fatalf("a cancelled task's run was handed out: %+v", hello.Reassigned)
	}
	if r, l := h.run(lost.RunID), h.lease(lost.RunID); r.ObservedState != protocol.RunFailed || l.Status != store.LeaseReleased {
		t.Errorf("run %s, lease %s; want failed and released", r.ObservedState, l.Status)
	}
}

func TestReassignedPausedRunWaits(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	h.claim("linux-1")
	h.exec(owner, h.env("control.set_mode", taskTarget(res.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(res.ControlRevision)), SetControlMode{})
	hello := h.hello("linux-1")
	if len(hello.Reassigned) != 1 || hello.Reassigned[0].Desired != protocol.DesiredPaused {
		t.Errorf("reassigned = %+v, want one paused assignment", hello.Reassigned)
	}
}

func TestLateArtifactAfterExpiryAdvancesTask(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	// No start report reaches tend before the lease runs out.
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunFinished})
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit: %+v", op.Error)
	}
	if got := h.task(res.TaskID); got.Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Errorf("task = %s, want awaiting_acceptance", got.Lifecycle)
	}
}

func TestHelloRestoresStartingRun(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunStarting})
	if r := h.run(as.RunID); r.ObservedState != protocol.RunStarting {
		t.Fatalf("run = %s, want starting", r.ObservedState)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Errorf("start after reconcile: %+v", op.Error)
	}
}

func TestResumedInterruptedRunDeliversResult(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The runner fenced the worker and reports it interrupted, then resumes.
	h.hello("linux-1", protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if r := h.run(as.RunID); r.ObservedState != protocol.RunInterrupted {
		t.Fatalf("run = %s, want interrupted", r.ObservedState)
	}
	if _, err := h.app.RecordRunEvents(context.Background(), runPrincipal(as.RunID, as.Role), as.RunID,
		[]protocol.RunEvent{{Seq: 1, Kind: protocol.EventRunRunning}}); err != nil {
		t.Fatal(err)
	}
	if r := h.run(as.RunID); r.ObservedState != protocol.RunRunning {
		t.Errorf("resumed run = %s, want running", r.ObservedState)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("artifact from the resumed run: %+v", op.Error)
	}
}
