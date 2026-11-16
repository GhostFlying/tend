package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func pendingSharedReviewSupplementForTest(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.SharedDeliveryReview, string) {
	t.Helper()
	h, runtime, issue, accept, _ := acceptanceSharedReviewFixture(t)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"unit_test":"passed; no whole PR scope established"}`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(accept.TaskID)
	if !h.releaseResource(accept.TaskID, resource.WorkspaceID) {
		t.Fatal("original acceptance resource not released")
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if r.Status != "reviewing" || r.Rounds[0].ReviewerRole != protocol.RoleAccept || r.Rounds[0].AcceptanceID == "" || r.Rounds[0].Result != nil {
		t.Fatal("fixture did not reach the real narrow-acceptance boundary", r)
	}
	setPRPolicyForTest(t, h, issue.IssueID, "manual")
	supplement := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "supplement", DeliveryID: "pr", ReviewerTaskID: supplement}))
	return h, runtime, issue, r, supplement
}

func TestSharedReviewSupplementWaitCannotHideFindingsDisposition(t *testing.T) {
	h, runtime, issue, r, supplement := pendingSharedReviewSupplementForTest(t)
	original := r.Rounds[0]
	executeSharedReviewForTest(t, h, runtime, r, supplement, "changes_requested")
	resource := h.resourceForTask(supplement)
	if !h.releaseResource(supplement, resource.WorkspaceID) {
		t.Fatal("supplement resource not released")
	}
	before := h.count("shared_delivery_review_history")
	op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)})
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition || h.count("shared_delivery_review_history") != before {
		t.Fatal("wait hid the completed supplemental changes_requested verdict", op)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	last := r.Rounds[0]
	if r.Status != "changes_requested" || r.Valid || last.AcceptanceID != original.AcceptanceID || last.AcceptanceVerdict != original.AcceptanceVerdict || last.RunID != original.RunID || last.ArtifactID != original.ArtifactID || last.SupplementRunID == "" || last.SupplementArtifactID == "" || last.Result == nil || len(last.Result.Findings) != 1 || last.Result.Findings[0].ID != "F1" {
		t.Fatal("the actual supplement or original acceptance was lost", r)
	}
	if op := continueTaskForTest(t, h, supplement, "Recheck the same candidate after consolidating the actual findings"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: supplement}), protocol.CodeInvalidTransition)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Consolidated correction preserves the candidate invariant"}}}))
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: supplement}))
	executeSharedReviewForTest(t, h, runtime, r, supplement, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || r.Status != "passed" || len(r.Rounds) != 2 || r.Rounds[0].Result.Findings[0].ID != "F1" || r.Rounds[1].TaskID != supplement || r.Rounds[1].RunID == last.SupplementRunID {
		t.Fatal("normal disposition and same-reviewer continuation failed", r)
	}
}

func TestSharedReviewSupplementWaitRequiresRecordingActualVerdict(t *testing.T) {
	for _, verdict := range []string{"passed", "blocked", "inconclusive"} {
		t.Run(verdict, func(t *testing.T) {
			h, runtime, issue, r, supplement := pendingSharedReviewSupplementForTest(t)
			original := r.Rounds[0]
			executeSharedReviewForTest(t, h, runtime, r, supplement, verdict)
			resource := h.resourceForTask(supplement)
			if !h.releaseResource(supplement, resource.WorkspaceID) {
				t.Fatal("supplement resource not released")
			}
			before := h.count("shared_delivery_review_history")
			op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)})
			if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition || h.count("shared_delivery_review_history") != before {
				t.Fatal("available supplemental quality was replaced by coordination waiting", verdict, op)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			want := "waiting"
			if verdict == "passed" {
				want = "passed"
			}
			last := r.Rounds[0]
			if r.Status != want || r.Valid != (verdict == "passed") || last.Result == nil || last.Result.Verdict != verdict || last.AcceptanceID != original.AcceptanceID || last.AcceptanceVerdict != original.AcceptanceVerdict || last.RunID != original.RunID || last.ArtifactID != original.ArtifactID || last.SupplementRunID == "" || last.SupplementArtifactID == "" {
				t.Fatal("normal recording lost the supplement or original immutable acceptance", r)
			}
		})
	}
}

func TestSharedReviewSupplementWaitPreservesUnsettledExecutionAndNoVerdict(t *testing.T) {
	for _, state := range []string{"active", "pending", "unknown", "coordination_only"} {
		t.Run(state, func(t *testing.T) {
			h, runtime, issue, r, supplement := pendingSharedReviewSupplementForTest(t)
			run, ok := h.claimInputRun(runtime)
			if !ok || run.TaskID != supplement {
				t.Fatal(run)
			}
			if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			request := ""
			if state != "active" {
				if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The supplemental coordination stopped without reaching a quality verdict"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				resource := h.resourceForTask(supplement)
				if !h.releaseResource(supplement, resource.WorkspaceID) {
					t.Fatal("supplement resource not released")
				}
				if state == "pending" || state == "unknown" {
					request = h.ids.New(protocol.PrefixRequest)
					// An unsettled dispatched side effect is an explicit failure
					// boundary fixture; wait itself uses the normal controlled API.
					if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
						_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'start','{}',?,?)`, request, run.RunID, state, h.clock.Now().UnixMilli())
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, runs, tasks := h.count("shared_delivery_review_history"), h.count("runs"), h.count("tasks")
			current := canonicalTaskIssue(t, h, issue.IssueID)
			wait := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{Reason: "Retain stopped supplemental coordination without inventing quality", ExpectedAssignmentEpoch: current.AssignmentEpoch}, Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: rev(r.Revision)}
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, wait, rev(current.Revision)), ReviewIssueDelivery{})
			if state != "coordination_only" {
				if technicalWaitErrorCode(op, err) != protocol.CodeUnknownOutcome || h.count("shared_delivery_review_history") != before || h.count("runs") != runs || h.count("tasks") != tasks {
					t.Fatal("unsettled supplement gained coordination recovery", op, err)
				}
				if request != "" {
					if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
						var status string
						if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE request_id=?`, request).Scan(&status); err != nil {
							return err
						}
						if status != state {
							t.Fatal("original native outcome was erased", status)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			waiting := sharedReviewResult(t, op)
			if waiting.Status != "waiting" || waiting.Valid || !reflect.DeepEqual(waiting.Rounds, r.Rounds) || waiting.Rounds[0].Result != nil {
				t.Fatal("supplement waiting fabricated quality or erased original provenance", waiting)
			}
			if op := continueTaskForTest(t, h, supplement, "Continue the original supplemental reviewer Task with the full frozen scope"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: supplement}))
			executeSharedReviewForTest(t, h, runtime, r, supplement, "passed")
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || r.Status != "passed" || len(r.Rounds) != 2 || r.Rounds[0].Result != nil || r.Rounds[1].TaskID != supplement || r.Rounds[1].RunID == run.RunID {
				t.Fatal("same-reviewer recovery did not record the actual new conclusion", r)
			}
		})
	}
}
