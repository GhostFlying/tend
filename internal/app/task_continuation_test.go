package app

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func continueTaskForTest(t *testing.T, h *harness, task, reason string) protocol.Operation {
	t.Helper()
	return h.exec(owner, h.env("tasks.continue", protocol.Target{Type: "task", ID: task}, protocol.ContinueTaskPayload{Reason: reason}, rev(h.task(task).ControlRevision)), ContinueTask{})
}

func TestTaskContinuationRetainsSpecAcceptanceAndRequestIdentity(t *testing.T) {
	h := newDeliverableHarness(t)
	created := h.createReportTask("persistent investigation", temporaryReportSpec())
	first, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("claim")
	}
	h.reportDirectory(first, t.TempDir())
	h.completeReport(first)
	old := h.task(created.TaskID)
	resource := h.resourceForTask(created.TaskID)
	blocked := continueTaskForTest(t, h, created.TaskID, "Investigate the remaining observation under the same goal")
	if protocol.CodeOf(blocked.Error) != protocol.CodeInvalidTransition {
		t.Fatal("missing physical release should retain uncertainty", blocked)
	}
	if !h.releaseResource(created.TaskID, resource.WorkspaceID) {
		t.Fatal("release")
	}
	env := h.env("tasks.continue", protocol.Target{Type: "task", ID: created.TaskID}, protocol.ContinueTaskPayload{Reason: "Investigate the remaining observation under the same goal"}, rev(old.ControlRevision))
	op := h.exec(owner, env, ContinueTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if replay := h.exec(owner, env, ContinueTask{}); replay.Status != protocol.OpFailed || protocol.CodeOf(replay.Error) != protocol.CodeStaleRevision {
		t.Fatal(replay)
	}
	if h.task(created.TaskID).SpecVersion != old.SpecVersion || h.task(created.TaskID).CurrentArtifactID != old.CurrentArtifactID || h.count("acceptances") != 1 || h.count("artifacts") != 1 || h.count("task_specs") != 1 {
		t.Fatal("continuation rewrote original completion")
	}
	if op := continueTaskForTest(t, h, created.TaskID, "duplicate continuation"); protocol.CodeOf(op.Error) != protocol.CodeInvalidTransition {
		t.Fatal(op)
	}
	next, ok, _ := h.claimDeliverableRun()
	if !ok || next.TaskID != created.TaskID || next.RunID == first.RunID || next.Generation != first.Generation+1 {
		t.Fatal(next)
	}
	if !strings.Contains(string(next.Spec), "Investigate the remaining observation") || !strings.Contains(string(next.Spec), old.CurrentArtifactID) {
		t.Fatal("fresh session cannot reconstruct continuation", string(next.Spec))
	}
	context, err := h.app.ReadRunContext(t.Context(), runPrincipal(next.RunID, next.Role), protocol.IssueMaterialsQuery{})
	if err != nil || context.ExecutionContext == nil || context.ExecutionContext.NextStep == "" || len(context.ExecutionContext.Artifacts) != 1 {
		t.Fatal(context, err)
	}
	h.reportDirectory(next, resource.Path)
	h.completeReport(next)
	if h.count("acceptances") != 2 || h.count("artifacts") != 2 || h.task(created.TaskID).ImplementContinuations != 1 {
		t.Fatal("history or continuation budget lost")
	}
}

func TestSharedReviewContinuesSameTaskAcrossCandidateChanges(t *testing.T) {
	h, runtime, issue, source, c, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	executeSharedReviewForTest(t, h, runtime, r, task, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	original := r.Rounds[0]
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("release")
	}
	for round := 0; round < 2; round++ {
		if op := continueTaskForTest(t, h, task, "Confirm rebase delta against the current shared PR review; reuse prior evidence"); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		c.HeadSHA = strings.Repeat(string(rune('c'+round)), 40)
		observeDeliveryForTest(t, h, issue.IssueID, c, "open", "")
		r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}))
		if h.task(task).SpecVersion != 1 || r.Rounds[len(r.Rounds)-1].TaskID != task || r.Rounds[len(r.Rounds)-1].ImplementGeneration != int64(round+2) {
			t.Fatal(r)
		}
		// A replacement native session receives the same durable context and can
		// recover the old immutable result while the same Task is now queued.
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			result, run, artifact, err := readSharedReviewResult(t.Context(), tx.Q(), r, original)
			if err != nil {
				return err
			}
			if result.HeadSHA != original.HeadSHA || run != original.RunID || artifact != original.ArtifactID {
				t.Fatal(result, run, artifact)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		executeSharedReviewForTest(t, h, runtime, r, task, "passed")
		r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
		if !r.Valid || len(r.Rounds) != round+2 || r.Rounds[0].ArtifactID != original.ArtifactID || r.Rounds[len(r.Rounds)-1].RunID == original.RunID {
			t.Fatal(r)
		}
		if !h.releaseResource(task, resource.WorkspaceID) {
			t.Fatal("release")
		}
	}
	if h.count("tasks") != 2 || h.task(task).ImplementContinuations != 2 {
		t.Fatal("recheck created another Task")
	}
	// Frozen source agreement and original report are still independently readable.
	var spec domain.Spec
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		raw, err := store.GetSpec(t.Context(), tx.Q(), task, 1)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &spec)
	}); err != nil || !strings.Contains(spec.Goal, "Review") {
		t.Fatal(spec, err)
	}
}

func TestTaskContinuationConcurrentRequestsAppendOneGeneration(t *testing.T) {
	h, _, _, task, _, _ := sharedReviewFixture(t)
	revision := h.task(task).ControlRevision
	envs := []*protocol.Envelope{
		h.env("tasks.continue", taskTarget(task), protocol.ContinueTaskPayload{Reason: "Same original goal"}, rev(revision)),
		h.env("tasks.continue", taskTarget(task), protocol.ContinueTaskPayload{Reason: "Same original goal"}, rev(revision)),
	}
	var wg sync.WaitGroup
	results := make([]protocol.Operation, 2)
	errors := make([]error, 2)
	for n := range envs {
		wg.Go(func() { results[n], errors[n] = h.app.Execute(t.Context(), owner, envs[n], ContinueTask{}) })
	}
	wg.Wait()
	passed, stale := 0, 0
	for n, result := range results {
		if errors[n] != nil {
			t.Fatal(errors[n])
		}
		switch {
		case result.Status == protocol.OpSucceeded:
			passed++
		case result.Error != nil && result.Error.Code == protocol.CodeStaleRevision:
			stale++
		default:
			t.Fatal(result)
		}
	}
	if passed != 1 || stale != 1 || h.task(task).ImplementGeneration != 2 || h.task(task).ImplementContinuations != 1 {
		t.Fatal(results, h.task(task))
	}
}

func TestTaskContinuationCannotReplayUnknownPhysicalWork(t *testing.T) {
	h, _, _, task, _, _ := sharedReviewFixture(t)
	before := h.task(task)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE task_id=? AND role='implement'`, task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := continueTaskForTest(t, h, task, "Continue the original goal")
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition || h.task(task).ImplementGeneration != before.ImplementGeneration {
		t.Fatal(op)
	}
	if h.countWhere(`SELECT COUNT(*) FROM runs WHERE task_id=? AND observed_state='unknown'`, task) != 1 {
		t.Fatal("continuation erased original unknown state")
	}
}
