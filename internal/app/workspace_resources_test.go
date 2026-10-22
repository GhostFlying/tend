package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func temporaryReportSpec() domain.Spec {
	return domain.Spec{Goal: "investigate", Acceptance: []string{"evidence independently checked"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary}}
}

func (h *harness) createReportTask(title string, spec domain.Spec) taskResult {
	h.t.Helper()
	op, task := h.createTask(owner, createTaskPayload{Title: title, Spec: spec})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("create %s: %+v", title, op)
	}
	return task
}
func (h *harness) resourceForTask(taskID string) store.WorkspaceRow {
	h.t.Helper()
	var w store.WorkspaceRow
	err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		w, err = store.GetWorkspaceByTask(context.Background(), tx.Q(), taskID)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}
func (h *harness) reportDirectory(as protocol.Assignment, path string) {
	h.t.Helper()
	op := h.runAs(as, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{ProviderID: "folder:investigation", ProviderInstanceID: "provider-session", Path: path, Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("report workspace: %+v", op)
	}
}
func (h *harness) completeReport(as protocol.Assignment, files ...protocol.ArtifactFile) {
	h.t.Helper()
	op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Investigation completed with evidence"}, Files: files}, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("report result: %+v", op)
	}
	accept, ok, _ := h.claimDeliverableRun()
	if !ok || accept.Role != protocol.RoleAccept || accept.TaskID != as.TaskID {
		h.t.Fatalf("acceptance: %+v %v", accept, ok)
	}
	if accept.Workspace.ID != as.Workspace.ID {
		h.t.Fatal("acceptance switched workspace")
	}
	op = h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("acceptance: %+v", op)
	}
}
func (h *harness) releaseResource(taskID, workspaceID string) bool {
	h.t.Helper()
	op := h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceRelease, protocol.Target{Type: "workspace", ID: workspaceID}, protocol.WorkspaceRelease{TaskID: taskID, WorkspaceID: workspaceID}, nil), ReleaseWorkspace{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("release workspace: %+v", op)
	}
	var result protocol.WorkspaceReleaseResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		h.t.Fatal(err)
	}
	return result.Released
}
func (h *harness) reserveResourceCleanup() []protocol.WorkspaceCleanup {
	h.t.Helper()
	op := h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceCleanup, protocol.Target{Type: "host", ID: "linux-1"}, protocol.WorkspaceCleanupRequest{}, nil), ReserveWorkspaceCleanup{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("reserve cleanup: %+v", op)
	}
	var result protocol.WorkspaceCleanupResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		h.t.Fatal(err)
	}
	return result.Workspaces
}

func TestWorkspaceTemporaryReportAndSerialReuseRequireStopConfirmation(t *testing.T) {
	h := newDeliverableHarness(t)
	first := h.createReportTask("original investigation", temporaryReportSpec())
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no temporary run")
	}
	if as.Workspace.Kind != "directory" || len(as.Workspace.Mounts) != 0 || as.Workspace.ID == "" || as.Workspace.TaskTitle != "original investigation" {
		t.Fatalf("temporary assignment: %+v", as.Workspace)
	}
	path := t.TempDir()
	h.reportDirectory(as, path)
	h.completeReport(as)
	if h.task(first.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("report-only task did not complete")
	}
	reuse := temporaryReportSpec()
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: first.TaskID}
	next := h.createReportTask("follow-up inspection", reuse)
	if _, ok, _ := h.claimDeliverableRun(); ok {
		t.Fatal("completed owner was replaced before runner confirmed stop")
	}
	if !h.releaseResource(first.TaskID, as.Workspace.ID) {
		t.Fatal("finished task could not release")
	}
	second, ok, _ := h.claimDeliverableRun()
	if !ok || second.TaskID != next.TaskID {
		t.Fatalf("reuse assignment: %+v %v", second, ok)
	}
	if second.Workspace.ID != as.Workspace.ID || second.Workspace.Name != as.Workspace.Name || second.Workspace.Path != path || !second.Workspace.Reused || second.Workspace.TaskTitle != "follow-up inspection" || second.TaskID == first.TaskID {
		t.Fatalf("resource identity or task handoff changed: %+v", second)
	}
	if got := h.run(second.RunID).HostID; got != "linux-1" {
		t.Fatalf("reuse moved hosts: %s", got)
	}
	if h.resourceForTask(next.TaskID).OccupiedBy != next.TaskID {
		t.Fatal("new task did not acquire exclusive ownership")
	}
	h.reportDirectory(second, path)
}

func TestWorkspacePausedDecisionAndUnknownRunsRetainOwnership(t *testing.T) {
	for _, state := range []string{"paused", "waiting_decision", "unknown"} {
		t.Run(state, func(t *testing.T) {
			h := newDeliverableHarness(t)
			first := h.createReportTask("owner", temporaryReportSpec())
			as, _, _ := h.claimDeliverableRun()
			h.reportDirectory(as, t.TempDir())
			switch state {
			case "paused":
				op := h.exec(owner, h.env("control.set_mode", taskTarget(first.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(first.TaskID).ControlRevision)), SetControlMode{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "waiting_decision":
				op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "needs owner decision"}, Release{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if h.task(first.TaskID).BlockReason != protocol.BlockWaitingDecision {
					t.Fatal("task did not wait for decision")
				}
			case "unknown":
				h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
				h.clock.Advance(DefaultLeaseTTL + time.Second)
				if _, err := h.app.ExpireLeases(context.Background()); err != nil {
					t.Fatal(err)
				}
				if h.run(as.RunID).ObservedState != protocol.RunUnknown {
					t.Fatal("run did not become unknown")
				}
				op := h.exec(owner, h.env("tasks.cancel", taskTarget(first.TaskID), map[string]any{}, rev(h.task(first.TaskID).ControlRevision)), CancelTask{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			reuse := temporaryReportSpec()
			reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: first.TaskID}
			h.createReportTask("waiting reuse", reuse)
			if h.releaseResource(first.TaskID, as.Workspace.ID) {
				t.Fatalf("%s owner released", state)
			}
			if _, ok, _ := h.claimDeliverableRun(); ok {
				t.Fatalf("reuse ran while %s", state)
			}
			h.clock.Advance(8 * 24 * time.Hour)
			if got := h.reserveResourceCleanup(); len(got) != 0 {
				t.Fatalf("live owner was collected: %+v", got)
			}
		})
	}
}

func TestWorkspaceRetentionPendingReuseAndExactCleanupReservation(t *testing.T) {
	h := newDeliverableHarness(t)
	first := h.createReportTask("temporary", temporaryReportSpec())
	as, _, _ := h.claimDeliverableRun()
	h.reportDirectory(as, t.TempDir())
	file, _ := h.uploadResultFile(as, "evidence.txt", []byte("retained evidence"))
	h.completeReport(as, file)
	if !h.releaseResource(first.TaskID, as.Workspace.ID) {
		t.Fatal("release failed")
	}
	h.clock.Advance(7*24*time.Hour - time.Second)
	if got := h.reserveResourceCleanup(); len(got) != 0 {
		t.Fatal("collected before retention expired")
	}
	reuse := temporaryReportSpec()
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: first.TaskID}
	pending := h.createReportTask("queued followup", reuse)
	h.clock.Advance(2 * time.Second)
	if got := h.reserveResourceCleanup(); len(got) != 0 {
		t.Fatal("collected resource with pending reuse binding")
	}
	op := h.exec(owner, h.env("tasks.cancel", taskTarget(pending.TaskID), map[string]any{}, rev(h.task(pending.TaskID).ControlRevision)), CancelTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	cleanup := h.reserveResourceCleanup()
	if len(cleanup) != 1 || cleanup[0].WorkspaceID != as.Workspace.ID || cleanup[0].Path == "" {
		t.Fatalf("cleanup reservation: %+v", cleanup)
	}
	h.withHosts("linux-1", "linux-2")
	_, foreignErr := h.app.Execute(context.Background(), runner("linux-2"), h.env(protocol.OpWorkspaceCleanupDone, protocol.Target{Type: "workspace", ID: as.Workspace.ID}, protocol.WorkspaceCleanupDone{WorkspaceID: as.Workspace.ID, CleanupID: cleanup[0].CleanupID}, nil), CompleteWorkspaceCleanup{})
	if protocol.CodeOf(foreignErr) != protocol.CodeForbidden {
		t.Fatalf("foreign host completed cleanup: %v", foreignErr)
	}
	repeated := h.reserveResourceCleanup()
	if len(repeated) != 1 || repeated[0] != cleanup[0] {
		t.Fatalf("cleanup identity changed: %+v", repeated)
	}
	op = h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceCleanupDone, protocol.Target{Type: "workspace", ID: as.Workspace.ID}, protocol.WorkspaceCleanupDone{WorkspaceID: as.Workspace.ID, CleanupID: "wrong"}, nil), CompleteWorkspaceCleanup{})
	if op.Status != protocol.OpFailed {
		t.Fatal("wrong cleanup identity accepted")
	}
	op, _ = h.createTask(owner, createTaskPayload{Title: "racing reuse", Spec: reuse})
	if op.Status != protocol.OpFailed {
		t.Fatal("reuse accepted during cleanup")
	}
	op = h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceCleanupDone, protocol.Target{Type: "workspace", ID: as.Workspace.ID}, protocol.WorkspaceCleanupDone{WorkspaceID: as.Workspace.ID, CleanupID: cleanup[0].CleanupID}, nil), CompleteWorkspaceCleanup{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	saved, err := h.app.DeliverableFile(context.Background(), owner, file.FileID)
	if err != nil || string(saved.Content) != "retained evidence" {
		t.Fatalf("cleanup removed delivery: %q %v", saved.Content, err)
	}
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		_, err := store.ReadFinalizedDeliverableFile(context.Background(), tx.Q(), file.FileID, 0, 16)
		return err
	}); err != nil {
		t.Fatalf("cleanup removed finalized artifact reference: %v", err)
	}
	op, _ = h.createTask(owner, createTaskPayload{Title: "removed reuse", Spec: reuse})
	if op.Status != protocol.OpFailed {
		t.Fatal("removed source silently recreated")
	}
	if got := h.reserveResourceCleanup(); len(got) != 0 {
		t.Fatalf("removed resource reappeared: %+v", got)
	}
}

func TestWorkspaceReuseBindingSurvivesSourceStartOver(t *testing.T) {
	h := newDeliverableHarness(t)
	first := h.createReportTask("source", temporaryReportSpec())
	old, _, _ := h.claimDeliverableRun()
	h.reportDirectory(old, t.TempDir())
	op := h.runAs(old, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Ready for acceptance"}}, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reuse := temporaryReportSpec()
	reuse.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: first.TaskID}
	pending := h.createReportTask("pinned reuse", reuse)
	h.withHosts("linux-2")
	h.checkWorkspaces()
	decisions := h.openDecisions(first.TaskID)
	if len(decisions) != 1 {
		t.Fatalf("source decisions: %+v", decisions)
	}
	if answer := h.answer(decisions[0], domain.StartOverOption); answer.Effect == nil || !answer.Effect.Applied {
		t.Fatalf("start over: %+v", answer)
	}
	h.exec(runner("linux-2"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-2"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil), Hello{})
	// The source is now free to choose a different repository layout. A
	// metadata edit of the pinned follow-up must retain its original directory.
	replacementSpec := temporaryReportSpec()
	replacementSpec.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(first.TaskID), updateIntentPayload{Spec: &replacementSpec}, rev(h.task(first.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("change source after start-over: %+v", op)
	}
	edited := reuse
	edited.Scope = "Check one additional observation"
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(pending.TaskID), updateIntentPayload{Spec: &edited}, rev(h.task(pending.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("edit pinned follow-up: %+v", op)
	}
	pinnedVersion := h.task(pending.TaskID).SpecVersion
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		body, err := store.GetSpec(context.Background(), tx.Q(), pending.TaskID, pinnedVersion)
		if err != nil {
			return err
		}
		var pinned domain.Spec
		if err := json.Unmarshal([]byte(body), &pinned); err != nil {
			return err
		}
		if len(pinned.Workspace.Mounts) != 0 || pinned.Workspace.ModeValue() != domain.WorkspaceReuse {
			t.Fatalf("pinned resource inherited replacement source layout: %+v", pinned.Workspace)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.exec(runner("linux-2"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-2"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil), Hello{})
	replacement, ok, _ := h.claim("linux-2")
	if !ok || replacement.TaskID != first.TaskID || replacement.Workspace.ID == old.Workspace.ID {
		t.Fatalf("replacement source: %+v %v", replacement, ok)
	}
	if got := h.resourceForTask(pending.TaskID); got.WorkspaceID != old.Workspace.ID || got.HostID != "linux-1" {
		t.Fatalf("pinned binding retargeted: %+v", got)
	}
}

func TestWorkspaceCleanupRotatesPastDeferredFullBatch(t *testing.T) {
	h := newDeliverableHarness(t)
	// Eleven real completed resources exceed the runner's batch of ten.
	ids := map[string]bool{}
	for range 11 {
		task := h.createReportTask("retention fairness", temporaryReportSpec())
		as, ok, _ := h.claimDeliverableRun()
		if !ok {
			t.Fatal("no run")
		}
		h.reportDirectory(as, t.TempDir())
		h.completeReport(as)
		if !h.releaseResource(task.TaskID, as.Workspace.ID) {
			t.Fatal("release failed")
		}
		ids[as.Workspace.ID] = true
	}
	h.clock.Advance(8 * 24 * time.Hour)
	reserve := func() []protocol.WorkspaceCleanup {
		op := h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceCleanup, protocol.Target{Type: "host", ID: "linux-1"}, protocol.WorkspaceCleanupRequest{Limit: 10}, nil), ReserveWorkspaceCleanup{})
		if op.Status != protocol.OpSucceeded {
			t.Fatalf("reserve: %+v", op)
		}
		var result protocol.WorkspaceCleanupResult
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result.Workspaces
	}
	first := reserve()
	if len(first) != 10 {
		t.Fatalf("first batch: %d", len(first))
	}
	reservations := map[string]string{}
	for _, w := range first {
		reservations[w.WorkspaceID] = w.CleanupID
	}
	// No physical cleanup succeeds. The next visit must still reach resource 11.
	h.clock.Advance(time.Minute)
	second := reserve()
	if len(second) != 10 {
		t.Fatalf("second batch: %d", len(second))
	}
	newResource := false
	for _, w := range second {
		if previous, exists := reservations[w.WorkspaceID]; exists && previous != w.CleanupID {
			t.Fatal("retry changed cleanup identity")
		}
		if _, exists := reservations[w.WorkspaceID]; !exists {
			newResource = true
		}
		reservations[w.WorkspaceID] = w.CleanupID
	}
	if !newResource || len(reservations) != len(ids) {
		t.Fatalf("old blocked batch starved later resource: %d", len(reservations))
	}
	// Even repeated wakeups within one clock tick rotate fairly, and retry
	// touches survive the app's transaction/no-effect handling.
	previousBatch := second
	for range 3 {
		previousIDs := map[string]bool{}
		for _, w := range previousBatch {
			previousIDs[w.WorkspaceID] = true
		}
		next := reserve()
		reachedDeferredOutsideBatch := false
		for _, w := range next {
			if reservations[w.WorkspaceID] != w.CleanupID {
				t.Fatal("retry lost durable authorization")
			}
			if !previousIDs[w.WorkspaceID] {
				reachedDeferredOutsideBatch = true
			}
		}
		if !reachedDeferredOutsideBatch {
			t.Fatal("same clock tick starved the resource outside previous batch")
		}
		previousBatch = next
	}
}
