package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// withHosts configures the given linux hosts, all serving every
// repository.
func (h *harness) withHosts(ids ...string) {
	h.t.Helper()
	c := testConfig()
	c.Hosts = nil
	for i, id := range ids {
		c.Hosts = append(c.Hosts, HostConfig{HostID: id, OS: "linux", Arch: "amd64", Repos: []string{"tend", "douyin", "template-assembler"},
			Slots: 2, TokenHash: strings.Repeat(string(rune('a'+i)), 64), Nodes: []string{"tag:tend-runner"}})
	}
	h.syncConfig(c)
}

// awaitingAcceptanceOn implements a new task on host and leaves its
// acceptance waiting to be claimed there. It returns the task and the
// implementation run.
func (h *harness) awaitingAcceptanceOn(host string) (string, protocol.Assignment) {
	h.t.Helper()
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, ok, _ := h.claim(host)
	if !ok {
		h.t.Fatal("nothing to claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(as, "runs.submit_artifact", testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		h.t.Fatalf("submit_artifact: %+v", op)
	}
	return res.TaskID, as
}

// reportWorkspace reports the implement run's workspace as laid out from
// baseSHA.
func (h *harness) reportWorkspace(as protocol.Assignment) protocol.Operation {
	h.t.Helper()
	branch := domain.TaskBranch(h.task(as.TaskID).WorkspaceName)
	report := protocol.WorkspaceReport{Mounts: []protocol.ResolvedMount{{Path: "tend", ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: branch}}}
	return h.runAs(as, protocol.OpRunsReportWorkspace, report, ReportWorkspace{})
}

func (h *harness) checkWorkspaces() []string {
	h.t.Helper()
	tasks, err := h.app.CheckWorkspaces(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return tasks
}

// openDecisions returns a task's open decisions.
func (h *harness) openDecisions(taskID string) []store.DecisionRow {
	h.t.Helper()
	var out []store.DecisionRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		all, err := store.ListDecisions(context.Background(), tx.Q(), taskID)
		for _, d := range all {
			if d.Status == store.DecisionOpen {
				out = append(out, d)
			}
		}
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func TestFault_WorkspaceHostRetired(t *testing.T) {
	h := newHarness(t)
	h.withHosts("linux-1", "linux-2")
	task, old := h.awaitingAcceptanceOn("linux-1")
	h.withHosts("linux-2")

	if got := h.checkWorkspaces(); !slices.Equal(got, []string{task}) {
		t.Fatalf("check blocked %v, want %s", got, task)
	}
	if got := h.task(task).BlockReason; got != protocol.BlockWorkspaceUnavailable {
		t.Errorf("block = %s", got)
	}
	decs := h.openDecisions(task)
	if len(decs) != 1 || !strings.Contains(decs[0].Question, "linux-1, which was retired") || !strings.Contains(decs[0].OptionsJSON, domain.StartOverOption) {
		t.Fatalf("decisions = %+v", decs)
	}
	if got := h.checkWorkspaces(); len(got) != 0 {
		t.Errorf("a second check changed %v", got)
	}
	if _, ok, _ := h.claim("linux-2"); ok {
		t.Error("the blocked acceptance was claimed elsewhere")
	}

	res := h.answer(decs[0], domain.StartOverOption)
	if res.Effect == nil || !res.Effect.Applied {
		t.Fatalf("start over = %+v", res.Effect)
	}
	got := h.task(task)
	if got.Lifecycle != protocol.TaskQueued || got.ImplementGeneration != 2 || got.CurrentArtifactID != "" || got.BlockReason != protocol.BlockNone || got.WorkspaceAssigned {
		t.Errorf("task after starting over = %+v", got)
	}
	if n := h.count("abandoned_workspaces"); n != 1 {
		t.Errorf("abandoned workspaces = %d, want 1", n)
	}
	if op := h.reportWorkspace(old); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Errorf("the old run reported a workspace the task gave up: %+v", op)
	}
	// The old host is back in the configuration, but its abandoned
	// workspace is not taken up again.
	h.withHosts("linux-1", "linux-2")
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Error("the task went back to the host it gave up")
	}
	as, ok, _ := h.claim("linux-2")
	if !ok || as.TaskID != task || as.Role != protocol.RoleImplement || as.Generation != 2 {
		t.Fatalf("claim on linux-2 = %+v (ok %v), want implementation 2", as, ok)
	}
	// A late report of the old run must not lay out the new workspace.
	if op := h.reportWorkspace(old); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Errorf("the old run reported on the new workspace: %+v", op)
	}
	if op := h.reportWorkspace(as); op.Status != protocol.OpSucceeded {
		t.Errorf("the new run's report: %+v", op)
	}
}

func TestFault_WorkspaceQuestionAskedAgain(t *testing.T) {
	h := newHarness(t)
	h.withHosts("linux-1", "linux-2")
	task, _ := h.awaitingAcceptanceOn("linux-1")
	h.withHosts("linux-2")
	h.checkWorkspaces()
	stale := h.openDecisions(task)[0]

	// A rating moves the task on, so the question no longer applies.
	rate := protocol.RateTaskPayload{Rating: protocol.RatingP1}
	if op := h.exec(owner, h.env("tasks.rate", taskTarget(task), rate, rev(h.task(task).ControlRevision)), RateTask{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("rate: %+v", op)
	}
	if res := h.answer(stale, domain.StartOverOption); res.Effect == nil || res.Effect.Applied {
		t.Fatalf("stale start over = %+v", res.Effect)
	}
	if got := h.checkWorkspaces(); !slices.Equal(got, []string{task}) {
		t.Fatalf("check changed %v, want the question asked again for %s", got, task)
	}
	decs := h.openDecisions(task)
	if len(decs) != 1 || decs[0].AskedRevision.Int64 != h.task(task).ControlRevision {
		t.Fatalf("decisions = %+v", decs)
	}
	if got := h.checkWorkspaces(); len(got) != 0 {
		t.Errorf("an open question was asked again: %v", got)
	}

	// Waiting, chosen for the task as it is, is not asked about again.
	h.answer(decs[0], domain.WaitForHostOption)
	if got := h.checkWorkspaces(); len(got) != 0 {
		t.Errorf("an answered question was asked again: %v", got)
	}
	if got := h.task(task).BlockReason; got != protocol.BlockWorkspaceUnavailable {
		t.Errorf("block = %s", got)
	}
}

func TestFault_WorkspaceHostRetiredMidRun(t *testing.T) {
	h := newHarness(t)
	h.withHosts("linux-1", "linux-2")
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	old, _, _ := h.claim("linux-1")
	h.runAs(old, protocol.OpRunsReportStarted, nil, ReportRunStarted{})

	// The host goes away mid-run: its lease runs out and nothing is left to
	// claim, but the run can only be found out on the retired host.
	h.withHosts("linux-2")
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.checkWorkspaces(); !slices.Equal(got, []string{res.TaskID}) {
		t.Fatalf("check blocked %v, want %s", got, res.TaskID)
	}
	decs := h.openDecisions(res.TaskID)
	if len(decs) != 1 {
		t.Fatalf("decisions = %+v", decs)
	}
	if r := h.answer(decs[0], domain.StartOverOption); r.Effect == nil || !r.Effect.Applied {
		t.Fatalf("start over = %+v", r.Effect)
	}
	as, ok, _ := h.claim("linux-2")
	if !ok || as.TaskID != res.TaskID || as.Role != protocol.RoleImplement || as.Generation != 2 {
		t.Errorf("claim on linux-2 = %+v (ok %v), want implementation 2", as, ok)
	}
}

func TestFault_WorkspaceHostReturns(t *testing.T) {
	h := newHarness(t)
	h.hello("linux-1")
	task, _ := h.awaitingAcceptanceOn("linux-1")
	h.clock.Advance(DefaultHostLostAfter - time.Minute)
	if got := h.checkWorkspaces(); len(got) != 0 {
		t.Fatalf("blocked %v before the host counts as lost", got)
	}
	h.clock.Advance(2 * time.Minute)
	if got := h.checkWorkspaces(); !slices.Equal(got, []string{task}) {
		t.Fatalf("check blocked %v, want %s", got, task)
	}
	if decs := h.openDecisions(task); len(decs) != 1 || !strings.Contains(decs[0].Question, "has not been heard from since") {
		t.Fatalf("decisions = %+v", decs)
	}

	// The runner restarts, or its claims get through again.
	if err := h.app.SeeHost(context.Background(), "linux-1"); err != nil {
		t.Fatal(err)
	}
	if got := h.checkWorkspaces(); !slices.Equal(got, []string{task}) {
		t.Fatalf("check unblocked %v, want %s", got, task)
	}
	if got := h.task(task).BlockReason; got != protocol.BlockNone {
		t.Errorf("block after the host came back = %s", got)
	}
	if decs := h.openDecisions(task); len(decs) != 0 {
		t.Errorf("decisions left open: %+v", decs)
	}
	if n := len(h.events("decision.answered")); n != 1 {
		t.Errorf("decision.answered events = %d, want 1", n)
	}
	if as, ok, _ := h.claim("linux-1"); !ok || as.TaskID != task || as.Role != protocol.RoleAccept {
		t.Errorf("claim = %+v (ok %v), want the acceptance", as, ok)
	}
}
