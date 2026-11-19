package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func frozenSharedReportForTest(t *testing.T, source string) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.SharedDeliveryReview, string) {
	t.Helper()
	if source == "supplement" {
		return pendingSharedReviewSupplementForTest(t)
	}
	h, runtime, issue, task, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: task}))
	reviewer := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: reviewer}))
	return h, runtime, issue, r, reviewer
}

func releaseSharedReviewerForTest(t *testing.T, h *harness, task string) {
	t.Helper()
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("completed reviewer resource not released")
	}
}

func TestFrozenSharedReviewVerdictSurvivesTaskContinuationBeforeRecord(t *testing.T) {
	for _, source := range []string{"report", "supplement"} {
		t.Run(source, func(t *testing.T) {
			h, runtime, issue, r, task := frozenSharedReportForTest(t, source)
			executeSharedReviewForTest(t, h, runtime, r, task, "changes_requested")
			artifact := h.task(task).CurrentArtifactID
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "Continue the same reviewer Task without rewriting its prior finding"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			before := h.count("shared_delivery_review_history")
			op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)})
			if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition || h.count("shared_delivery_review_history") != before {
				t.Fatal("wait hid the frozen changes_requested verdict after normal Task continuation", source, op)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			last := r.Rounds[0]
			recorded := last.ArtifactID
			if source == "supplement" {
				recorded = last.SupplementArtifactID
			}
			if r.Status != "changes_requested" || r.Valid || recorded != artifact || last.Result == nil || len(last.Result.Findings) != 1 || last.Result.Findings[0].ID != "F1" {
				t.Fatal("the frozen actual finding could not record after continuation", r)
			}
			expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}), protocol.CodeInvalidTransition)
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Consolidated correction of the original immutable finding"}}}))
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}))
			executeSharedReviewForTest(t, h, runtime, r, task, "passed")
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || r.Status != "passed" || len(r.Rounds) != 2 || r.Rounds[0].Result.Findings[0].ID != "F1" || r.Rounds[1].TaskID != task {
				t.Fatal("normal disposition and focused same-Task recheck failed", r)
			}
		})
	}
}

func TestSharedReviewRecheckAllowsSameGenerationInNewSpec(t *testing.T) {
	for _, source := range []string{"report", "supplement"} {
		t.Run(source, func(t *testing.T) {
			h, runtime, issue, review, task := frozenSharedReportForTest(t, source)
			executeSharedReviewForTest(t, h, runtime, review, task, "changes_requested")
			releaseSharedReviewerForTest(t, h, task)
			review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			original := protocol.SharedReviewInputRound(review.Rounds[0])
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{
				Action: "resolve", DeliveryID: "pr",
				Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Consolidated correction"}},
			}))
			recheck := protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}
			expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, recheck), protocol.CodeInvalidTransition)

			if op := continueTaskForTest(t, h, task, "Repeat the independent check under the updated instructions"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			current := h.task(task)
			spec := h.storedSpec(task, current.SpecVersion)
			spec.Goal += "; verify the corrected candidate"
			if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(current.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			executeSharedReviewForTest(t, h, runtime, review, task, "passed")
			newer := h.task(task)
			if newer.SpecVersion <= original.SpecVersion || newer.ImplementGeneration != original.ImplementGeneration {
				t.Fatal("expected a new spec with the same generation", original, newer)
			}

			review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, recheck))
			review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !review.Valid || review.Status != "passed" || len(review.Rounds) != 2 {
				t.Fatal("the new execution did not produce an independent formal review round", review)
			}
			old, next := protocol.SharedReviewInputRound(review.Rounds[0]), review.Rounds[1]
			if old.RunID == "" || old.RunID != original.RunID || old.ArtifactID != original.ArtifactID ||
				next.RunID == "" || next.RunID == old.RunID || next.TaskID != task || next.SpecVersion != newer.SpecVersion ||
				next.ImplementGeneration != old.ImplementGeneration || next.ArtifactID != newer.CurrentArtifactID {
				t.Fatal("original or new execution identity was not preserved", old, next)
			}
		})
	}
}

func sharedRoundArtifactForTest(source string, round protocol.SharedDeliveryReviewRound) string {
	if source == "supplement" {
		return round.SupplementArtifactID
	}
	return round.ArtifactID
}

func TestFrozenSharedReviewRecordsOriginalBeforeNewerArtifact(t *testing.T) {
	for _, source := range []string{"report", "supplement"} {
		t.Run(source, func(t *testing.T) {
			h, runtime, issue, r, task := frozenSharedReportForTest(t, source)
			executeSharedReviewForTest(t, h, runtime, r, task, "changes_requested")
			original := h.task(task).CurrentArtifactID
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "Produce a real later generation without rewriting the frozen review"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			executeSharedReviewForTest(t, h, runtime, r, task, "passed")
			newer := h.task(task).CurrentArtifactID
			releaseSharedReviewerForTest(t, h, task)
			if newer == original {
				t.Fatal("fixture did not produce an actual different generation")
			}
			expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}), protocol.CodeInvalidTransition)
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if r.Status != "changes_requested" || r.Valid || sharedRoundArtifactForTest(source, r.Rounds[0]) != original || r.Rounds[0].Result.Findings[0].ID != "F1" || h.task(task).CurrentArtifactID != newer {
				t.Fatal("newer artifact replaced the original frozen finding", r)
			}
			expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}), protocol.CodeInvalidTransition)
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "The later actual generation addresses the frozen finding"}}}))
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}))
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || len(r.Rounds) != 2 || r.Rounds[1].ArtifactID != newer || r.Rounds[0].Result.Findings[0].ID != "F1" {
				t.Fatal("later generation could not be admitted as its own real recheck", r)
			}
		})
	}
}

func TestFrozenSharedReviewPassedReportRecordsAfterContinuation(t *testing.T) {
	for _, source := range []string{"report", "supplement"} {
		t.Run(source, func(t *testing.T) {
			h, runtime, issue, r, task := frozenSharedReportForTest(t, source)
			executeSharedReviewForTest(t, h, runtime, r, task, "passed")
			original := h.task(task).CurrentArtifactID
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "Keep the completed original result while the sustained Task continues"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || r.Status != "passed" || sharedRoundArtifactForTest(source, r.Rounds[0]) != original || r.Rounds[0].Result.Verdict != "passed" {
				t.Fatal("normal continuation blocked the actual frozen passed result", r)
			}
		})
	}
}

func submitSharedCoordinationForTest(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, task string) {
	t.Helper()
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run)
	}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Coordination ended without a quality result"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestFrozenSharedReviewNoQualityCannotConsumeNewGeneration(t *testing.T) {
	for _, source := range []string{"report", "supplement"} {
		t.Run(source, func(t *testing.T) {
			h, runtime, issue, r, task := frozenSharedReportForTest(t, source)
			submitSharedCoordinationForTest(t, h, runtime, task)
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "Continue coordination using a new real generation"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			executeSharedReviewForTest(t, h, runtime, r, task, "passed")
			newer := h.task(task).CurrentArtifactID
			releaseSharedReviewerForTest(t, h, task)
			expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}), protocol.CodeInvalidRequest)
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}))
			if r.Valid || r.Status != "waiting" || r.Rounds[0].Result != nil {
				t.Fatal("new generation masqueraded as old quality or normal waiting was blocked", r)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: task}))
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || len(r.Rounds) != 2 || r.Rounds[0].Result != nil || r.Rounds[1].ArtifactID != newer {
				t.Fatal("new generation failed its own normal binding", r)
			}
		})
	}
}

func TestFrozenSharedReviewSourceGapsRetainUnknown(t *testing.T) {
	for _, gap := range []string{"execution_identity", "completion", "ambiguous_completion"} {
		t.Run(gap, func(t *testing.T) {
			h, runtime, issue, r, task := frozenSharedReportForTest(t, "report")
			executeSharedReviewForTest(t, h, runtime, r, task, "changes_requested")
			artifact := h.task(task).CurrentArtifactID
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "Preserve original evidence when retained source history is incomplete"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// These explicit retained-history corruption boundaries are not
			// business recovery steps. The production API must not guess them.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch gap {
				case "execution_identity":
					_, err := tx.Exec(t.Context(), `UPDATE shared_delivery_reviews SET body_json=json_set(body_json,'$.rounds[0].implement_generation',0) WHERE review_id=?`, r.ReviewID)
					return err
				case "completion":
					_, err := tx.Exec(t.Context(), `UPDATE events SET body_json=json_remove(body_json,'$.artifact_id') WHERE object_type='task' AND object_id=? AND kind='task.completed' AND json_extract(body_json,'$.artifact_id')=?`, task, artifact)
					return err
				default:
					clone := h.ids.New(protocol.PrefixArtifact)
					if _, err := tx.Exec(t.Context(), `INSERT INTO artifacts(artifact_id,task_id,run_id,kind,ref_json,digest,created_at) SELECT ?,task_id,run_id,kind,ref_json,digest,created_at FROM artifacts WHERE artifact_id=?`, clone, artifact); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO events(event_id,source,object_type,object_id,kind,body_json,created_at) VALUES(?,'retained-fixture','task',?,'task.completed',json_object('artifact_id',?,'spec_version',?),?)`, h.ids.New(protocol.PrefixEvent), task, clone, r.Rounds[0].SpecVersion, h.clock.Now().UnixMilli())
					return err
				}
			}); err != nil {
				t.Fatal(err)
			}
			before := h.count("shared_delivery_review_history")
			for _, action := range []string{"wait", "record"} {
				current := canonicalTaskIssue(t, h, issue.IssueID)
				p := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{Reason: "Retain the exact missing original source", ExpectedAssignmentEpoch: current.AssignmentEpoch}, Action: action, DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}
				op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(current.Revision)), ReviewIssueDelivery{})
				if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome || h.count("shared_delivery_review_history") != before {
					t.Fatal("source gap was silently guessed or erased", gap, action, op, err)
				}
			}
		})
	}
}

func TestFrozenSharedReviewRetainsUniqueCompletedResultBesideStaleSubmission(t *testing.T) {
	h, runtime, issue, r, task := frozenSharedReportForTest(t, "report")
	executeSharedReviewForTest(t, h, runtime, r, task, "changes_requested")
	original := h.task(task).CurrentArtifactID
	releaseSharedReviewerForTest(t, h, task)
	if op := continueTaskForTest(t, h, task, "Retain the uniquely completed original review"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A historical stale submission has no completion authority. Preserve it
	// without guessing it over the uniquely completed frozen result.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO artifacts(artifact_id,task_id,run_id,kind,ref_json,digest,created_at) SELECT ?,task_id,run_id,kind,ref_json,digest,created_at FROM artifacts WHERE artifact_id=?`, h.ids.New(protocol.PrefixArtifact), original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}), protocol.CodeInvalidTransition)
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if r.Status != "changes_requested" || r.Rounds[0].ArtifactID != original || r.Rounds[0].Result.Findings[0].ID != "F1" {
		t.Fatal("a stale submission blocked or replaced the actual completed result", r)
	}
}
