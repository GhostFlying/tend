package app

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (h *harness) reconcileResources(host string) []protocol.WorkspaceReconcile {
	h.t.Helper()
	before := h.count("request_records")
	op := h.exec(runner(host), h.env(protocol.OpWorkspaceReconcile, protocol.Target{Type: "host", ID: host}, struct{}{}, nil), ReconcileWorkspaces{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("reconcile resources: %+v", op)
	}
	if h.count("request_records") != before {
		h.t.Fatal("read-only reconciliation recorded a mutation request")
	}
	var result protocol.WorkspaceReconcileResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		h.t.Fatal(err)
	}
	return result.Workspaces
}

func TestWorkspaceReconcileRequiresEveryActualTerminalRunAndHost(t *testing.T) {
	h := newDeliverableHarness(t)
	first := h.createReportTask("legacy-compatible result", temporaryReportSpec())
	as, _, _ := h.claimDeliverableRun()
	h.reportDirectory(as, t.TempDir())
	if got := h.reconcileResources("linux-1"); len(got) != 0 {
		t.Fatalf("active resource offered for stop reconciliation: %+v", got)
	}
	h.completeReport(as)
	candidates := h.reconcileResources("linux-1")
	if len(candidates) != 1 {
		t.Fatalf("completed resource absent: %+v", candidates)
	}
	item := candidates[0]
	if item.WorkspaceID != as.Workspace.ID || item.TaskID != first.TaskID || !item.TaskTerminal || len(item.RunIDs) != 2 || !slices.Contains(item.RunIDs, as.RunID) {
		t.Fatalf("all implementation and acceptance agents must be checked: %+v", item)
	}
	if !slices.Equal(item.FinishedRunIDs, item.RunIDs) {
		t.Fatalf("successful close-before-Wait facts missing: %+v", item)
	}
	if got := h.resourceForTask(first.TaskID).OccupiedBy; got != first.TaskID {
		t.Fatal("query released resource without process proof")
	}
	h.withHosts("linux-1", "linux-2")
	if got := h.reconcileResources("linux-2"); len(got) != 0 {
		t.Fatalf("foreign host received resource runs: %+v", got)
	}
	if !h.releaseResource(first.TaskID, as.Workspace.ID) {
		t.Fatal("confirmed resource not released")
	}
	if got := h.reconcileResources("linux-1"); len(got) != 0 {
		t.Fatalf("released resource offered again: %+v", got)
	}
}

func TestWorkspaceReconcileNeverTreatsCancelledUnknownRunAsStopped(t *testing.T) {
	h := newDeliverableHarness(t)
	first := h.createReportTask("unknown process", temporaryReportSpec())
	as, _, _ := h.claimDeliverableRun()
	h.reportDirectory(as, t.TempDir())
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("tasks.cancel", taskTarget(first.TaskID), map[string]any{}, rev(h.task(first.TaskID).ControlRevision)), CancelTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.reconcileResources("linux-1"); len(got) != 0 {
		t.Fatalf("unknown process treated as terminal: %+v", got)
	}
	op = h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "verified process stopped"}, Release{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	got := h.reconcileResources("linux-1")
	if len(got) != 1 || !got[0].TaskTerminal || !slices.Equal(got[0].RunIDs, []string{as.RunID}) || len(got[0].FinishedRunIDs) != 0 {
		t.Fatalf("resolved run missing from reconciliation: %+v", got)
	}
}

func TestWorkspaceReconcileSeparatesAbandonedAndReplacementRuns(t *testing.T) {
	h := newDeliverableHarness(t)
	first := h.createReportTask("start over", temporaryReportSpec())
	old, _, _ := h.claimDeliverableRun()
	h.reportDirectory(old, t.TempDir())
	op := h.runAs(old, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "original result"}}, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.withHosts("linux-2")
	h.checkWorkspaces()
	decisions := h.openDecisions(first.TaskID)
	if len(decisions) != 1 {
		t.Fatalf("start over decision: %+v", decisions)
	}
	if answer := h.answer(decisions[0], domain.StartOverOption); answer.Effect == nil || !answer.Effect.Applied {
		t.Fatalf("start over: %+v", answer)
	}
	h.exec(runner("linux-2"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-2"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil), Hello{})
	replacement, ok, _ := h.claim("linux-2")
	if !ok {
		t.Fatal("replacement not assigned")
	}
	h.withHosts("linux-1", "linux-2")
	got := h.reconcileResources("linux-1")
	if len(got) != 1 || got[0].TaskTerminal || got[0].WorkspaceID != old.Workspace.ID || !slices.Equal(got[0].RunIDs, []string{old.RunID}) {
		t.Fatalf("old run mapped to replacement resource: %+v", got)
	}
	if got := h.reconcileResources("linux-2"); len(got) != 0 {
		t.Fatalf("replacement live process offered: %+v", got)
	}
	if got := h.resourceForTask(first.TaskID); got.WorkspaceID != replacement.Workspace.ID {
		t.Fatal("reconcile query changed current binding")
	}
}

func TestWorkspaceReconcileRejectsOwnerPrincipal(t *testing.T) {
	h := newDeliverableHarness(t)
	_, err := h.app.Execute(context.Background(), owner, h.env(protocol.OpWorkspaceReconcile, protocol.Target{Type: "host", ID: "linux-1"}, struct{}{}, nil), ReconcileWorkspaces{})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("owner impersonated runner process proof: %v", err)
	}

}
