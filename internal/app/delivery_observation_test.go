package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func deliveryObservationFixture(t *testing.T) (*harness, protocol.Assignment, protocol.IssueDeliveryDuty) {
	t.Helper()
	h, issue, source, candidate := deliveryFixture(t)
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	answerDeliveryForTest(t, h, duty, "approve", "")
	runtime := testRuntime("delivery-observation")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	payload := protocol.CreateIssueTaskPayload{
		ExpectedAssignmentEpoch: issue.AssignmentEpoch,
		Title:                   "Execute approved delivery with run-scoped evidence",
		DeliveryAuthorization:   &protocol.IssueDeliveryAuthorization{TaskID: source, Revision: duty.Revision},
		Spec: protocol.OwnerTaskSpec{
			Goal:         "Verify the server-bound authority before the approved merge",
			Acceptance:   []string{"Retain the exact candidate and original approval"},
			Deliverables: []string{"report"},
			Workspace:    protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}},
			Observation:  &protocol.ObservationScope{WorkItemIDs: []string{issue.IssueID}, Deployment: true},
		},
	}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task.TaskID {
		t.Fatalf("delivery claim: %+v %v", as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, as, duty
}

func TestDeliveryObservationAndOwnerViewExposeFrozenAuthority(t *testing.T) {
	h, as, duty := deliveryObservationFixture(t)
	before := h.storedSpec(as.TaskID, as.SpecVersion)
	observation, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	readAuthority := func(value any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		authority, ok := decoded["delivery_authorization"].(map[string]any)
		if !ok {
			t.Fatalf("missing structured delivery authorization; task spec cannot contain the create-request binding: %s", raw)
		}
		if authority["source_task_id"] != duty.TaskID || authority["duty_revision"] != float64(duty.Revision) || authority["task_id"] != as.TaskID || authority["valid"] != true {
			t.Fatalf("wrong frozen authority: %+v", authority)
		}
		return authority
	}
	readAuthority(observation)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.Task(t.Context(), tx.Q(), as.TaskID)
		if err == nil {
			readAuthority(view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := h.storedSpec(as.TaskID, as.SpecVersion)
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("read changed the frozen delivery agreement")
	}
}

func TestDeliveryObservationInvalidatesCurrentAuthorityWithoutChangingCandidate(t *testing.T) {
	for _, change := range []string{"withdrawn_source", "edited_source", "decision_version", "decision_reopened", "conditional_answer", "head", "unknown_provider", "epoch", "duty_revision", "candidate"} {
		t.Run(change, func(t *testing.T) {
			h, as, duty := deliveryObservationFixture(t)
			before, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
			if err != nil || before.DeliveryAuthorization == nil || !before.DeliveryAuthorization.Valid {
				t.Fatalf("initial authority: %+v %v", before.DeliveryAuthorization, err)
			}
			frozen := before.DeliveryAuthorization.Candidate
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch change {
				case "withdrawn_source":
					_, err = tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
				case "edited_source":
					_, err = tx.Exec(t.Context(), `UPDATE message_dispositions SET content_revision=content_revision+1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
				case "decision_version":
					_, err = tx.Exec(t.Context(), `UPDATE decisions SET version=version+1 WHERE decision_id=(SELECT decision_id FROM attentions WHERE attention_id=?)`, duty.AttentionID)
				case "decision_reopened":
					_, err = tx.Exec(t.Context(), `UPDATE decisions SET status='open',version=version+1,answer_json=NULL,answered_message_id=NULL WHERE decision_id=(SELECT decision_id FROM attentions WHERE attention_id=?)`, duty.AttentionID)
				case "conditional_answer":
					_, err = tx.Exec(t.Context(), `UPDATE decisions SET answer_json='{"option":"approve","text":"new condition"}' WHERE decision_id=(SELECT decision_id FROM attentions WHERE attention_id=?)`, duty.AttentionID)
				case "head", "unknown_provider":
					var row store.DeliveryObservationRow
					row, err = store.GetDeliveryObservation(t.Context(), tx.Q(), before.DeliveryAuthorization.IssueID, frozen.DeliveryID)
					if err == nil {
						if change == "head" {
							row.Observation.HeadSHA = strings.Repeat("d", 40)
						} else {
							row.Observation.ErrorKind = "timeout"
						}
						err = tx.PutDeliveryObservation(t.Context(), row)
					}
				case "epoch":
					var issue protocol.Issue
					issue, err = store.GetCanonicalIssue(t.Context(), tx.Q(), before.DeliveryAuthorization.IssueID)
					if err == nil {
						previous := issue.Revision
						issue.Revision++
						issue.AssignmentEpoch++
						err = tx.SaveCanonicalIssue(t.Context(), issue, previous, store.IssueRevisionProvenance{Actor: owner, RequestID: "observation-epoch-change", Reason: "Change current responsibility without rewriting frozen execution"})
					}
				case "duty_revision", "candidate":
					var current protocol.IssueDeliveryDuty
					current, err = store.GetIssueDeliveryDuty(t.Context(), tx.Q(), before.DeliveryAuthorization.IssueID, duty.TaskID)
					if err == nil {
						if change == "duty_revision" {
							current.Revision++
						} else {
							current.Candidate.HeadSHA = strings.Repeat("e", 40)
						}
						err = tx.PutIssueDeliveryDuty(t.Context(), before.DeliveryAuthorization.IssueID, current)
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
			if err != nil {
				t.Fatal(err)
			}
			authority := view.DeliveryAuthorization
			if authority == nil || authority.Valid || authority.InvalidReason == "" || authority.Candidate.HeadSHA != frozen.HeadSHA || authority.DutyRevision != before.DeliveryAuthorization.DutyRevision {
				t.Fatalf("revoked authority or frozen identity lost: %+v", authority)
			}
			if authority.Approval == nil || authority.Approval.SourceMessageID != before.DeliveryAuthorization.Approval.SourceMessageID || authority.Approval.SourceRevision != before.DeliveryAuthorization.Approval.SourceRevision || authority.Approval.SourceRequestID == "" {
				t.Fatalf("original approval provenance was replaced by current decision state: %+v", authority.Approval)
			}
		})
	}
}

func TestDeliveryObservationReportsAdmissionSeparatelyFromMergeAuthority(t *testing.T) {
	h, as, _ := deliveryObservationFixture(t)
	h.app.observeDeployment = func(context.Context) (protocol.ObservedDeployment, error) {
		return protocol.ObservedDeployment{ProofStatus: "unconfirmed"}, nil
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		runtime, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		runtime.Runtime.PendingOperations, runtime.Runtime.PendingClaims = 7, 3
		return tx.PutHostRuntime(t.Context(), "linux-1", runtime)
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || !view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.Candidate.Deploy {
		t.Fatalf("deployment proof incorrectly denied merge-only authority: %+v %v", view, err)
	}
	if view.DeliveryAuthorization.RunID != as.RunID || view.DeliveryAuthorization.LeaseID != as.LeaseID || view.DeliveryAuthorization.Role != as.Role || view.DeliveryAuthorization.ObservedAt != view.ObservedAt {
		t.Fatalf("missing live run authority: %+v", view.DeliveryAuthorization)
	}
	runner := view.Deployment.Runner
	if runner == nil || runner.Source == "" || runner.PendingOperations == nil || *runner.PendingOperations != 7 || runner.PendingClaims == nil || *runner.PendingClaims != 3 || runner.LastHeartbeatAt == 0 {
		t.Fatalf("reported native unknowns were lost: %+v", runner)
	}
	if admission := view.Deployment.Admission; admission == nil || admission.Source == "" || admission.ObservedAt != view.ObservedAt || admission.ArchivePrepared {
		t.Fatalf("missing real admission sources: %+v", admission)
	}
}

func TestDeliveryObservationPreservesAuthorityAfterExactApprovedMerge(t *testing.T) {
	h, as, _ := deliveryObservationFixture(t)
	before, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	frozen := before.DeliveryAuthorization.Candidate
	merged := frozen
	merged.TargetSHA = strings.Repeat("c", 40)
	observeDeliveryForTest(t, h, before.DeliveryAuthorization.IssueID, merged, "merged", merged.TargetSHA)
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || !view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.Candidate != frozen || view.DeliveryAuthorization.Provider.State != "merged" || view.DeliveryAuthorization.Provider.MergeSHA != merged.TargetSHA {
		t.Fatalf("the execution's own merged result invalidated or rewrote the authority: %+v %v", view.DeliveryAuthorization, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), before.DeliveryAuthorization.IssueID, frozen.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.TargetContainsMerge = false
		return tx.PutDeliveryObservation(t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	view, err = h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.InvalidReason != "provider_candidate_changed" {
		t.Fatalf("unverified merged target remained authorized: %+v %v", view.DeliveryAuthorization, err)
	}
}

func TestDeliveryObservationHandoffKeepsBindingAndRejectsReleasedPredecessor(t *testing.T) {
	h, as, _ := deliveryObservationFixture(t)
	if !as.QuestionHandoffs {
		t.Fatal("handoff contract was not negotiated")
	}
	before, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	q := askQuestion(t, h, as, scopeQuestion)
	if op := h.runAs(as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: q.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("released predecessor still read delivery authority: %v", err)
	}
	if op := answerQuestion(t, h, q, scopeQuestion.Options[0], "Original typed approval retained; this answer supplies no new merge authority"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime := deliveryObservationRuntime(t, h)
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != as.TaskID || next.RunID == as.RunID || next.QuestionContinuation == nil {
		t.Fatalf("original agreement did not continue: %+v %v", next, ok)
	}
	if op := h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(next.RunID, next.Role))
	if err != nil || view.DeliveryAuthorization == nil || !view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.RunID != next.RunID || view.DeliveryAuthorization.LeaseID != next.LeaseID || view.DeliveryAuthorization.SourceTaskID != before.DeliveryAuthorization.SourceTaskID || view.DeliveryAuthorization.Candidate.HeadSHA != before.DeliveryAuthorization.Candidate.HeadSHA {
		t.Fatalf("successor lost exact authority: %+v %v", view.DeliveryAuthorization, err)
	}
}

func automaticDeliveryObservationFixture(t *testing.T) (*harness, protocol.Assignment, protocol.SharedDeliveryReview) {
	t.Helper()
	h, runtime, issue, source, candidate, acceptance := sharedReviewFixture(t)
	review := importSharedReviewForTest(t, h, issue, source, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	op := createDeliveryExecutionForTest(t, h, issue.IssueID, duty)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != created.TaskID {
		t.Fatalf("auto delivery claim: %+v %v", as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, as, review
}

func TestDeliveryObservationAutomaticGrantReportsPolicyWithoutInventingApproval(t *testing.T) {
	h, as, _ := automaticDeliveryObservationFixture(t)
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil {
		t.Fatalf("auto authority: %+v %v", view, err)
	}
	authority := view.DeliveryAuthorization
	if !authority.Valid || authority.Grant == nil || authority.Grant.Mode != "auto" || authority.Approval != nil || authority.Policy == nil || authority.Policy.Mode != "auto" || authority.Policy.RequestID == "" || authority.Policy.Digest != authority.Grant.PolicyDigest {
		t.Fatalf("policy was lost or became a synthetic approval: %+v", authority)
	}
	setPRPolicyForTest(t, h, authority.IssueID, "manual")
	view, err = h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.InvalidReason == "" || view.DeliveryAuthorization.Grant.Mode != "auto" || view.DeliveryAuthorization.Approval != nil {
		t.Fatalf("withdrawn policy stayed valid or rewrote the frozen grant: %+v %v", view.DeliveryAuthorization, err)
	}
}

func TestDeliveryObservationReportsActualChecksAndScopedReviewEvidence(t *testing.T) {
	h, as, expected := automaticDeliveryObservationFixture(t)
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view.DeliveryAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Provider struct {
			CI *protocol.DeliveryCI `json:"ci"`
		} `json:"provider"`
		Review *struct {
			ReviewID string `json:"review_id"`
			Valid    bool   `json:"valid"`
			Round    *struct {
				AcceptanceID    string `json:"acceptance_id"`
				RepositoryCheck string `json:"repository_check"`
			} `json:"round"`
		} `json:"shared_review"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider.CI == nil || got.Provider.CI.State != "success" || len(got.Provider.CI.Checks) != 2 || got.Review == nil || got.Review.ReviewID != expected.ReviewID || !got.Review.Valid || got.Review.Round == nil || got.Review.Round.AcceptanceID != expected.Rounds[0].AcceptanceID || got.Review.Round.RepositoryCheck == "" {
		t.Fatalf("missing actual provider checks or bound formal review evidence: %s", raw)
	}
	if strings.Contains(string(raw), `"links"`) {
		t.Fatalf("restricted worker view exposed the shared review's global links: %s", raw)
	}
}

func TestDeliveryObservationPreservesFailedAndUnknownChecks(t *testing.T) {
	h, as, _ := automaticDeliveryObservationFixture(t)
	first, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	identity := first.DeliveryAuthorization
	for _, state := range []string{"absent", "failure", "pending", "unknown", "unknown_check", "wrong_head", "error", "truncated", "skipped_only"} {
		t.Run(state, func(t *testing.T) {
			ci := &protocol.DeliveryCI{HeadSHA: identity.Candidate.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}
			switch state {
			case "absent":
				ci = nil
			case "failure", "pending", "unknown":
				ci.State, ci.Checks[0].Conclusion = state, state
			case "unknown_check":
				ci.Checks = append(ci.Checks, protocol.DeliveryCheck{Name: "unclassified", State: "completed", Conclusion: "unknown"})
			case "wrong_head":
				ci.HeadSHA = strings.Repeat("d", 40)
			case "error":
				ci.ErrorKind = "timeout"
			case "truncated":
				ci.Truncated = true
			case "skipped_only":
				ci.Checks[0].Conclusion = "skipped"
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), identity.IssueID, identity.Candidate.DeliveryID)
				if err != nil {
					return err
				}
				row.Observation.CI, row.Observation.Mergeability = ci, "unknown"
				return tx.PutDeliveryObservation(t.Context(), row)
			}); err != nil {
				t.Fatal(err)
			}
			view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
			if err != nil || view.DeliveryAuthorization == nil {
				t.Fatalf("current checks: %+v %v", view, err)
			}
			authority := view.DeliveryAuthorization
			if authority.Valid || authority.InvalidReason != "delivery_checks_not_current" || authority.Provider == nil || !reflect.DeepEqual(authority.Provider.CI, ci) || authority.Provider.Mergeability != "unknown" || authority.SharedReview == nil || !authority.SharedReview.Valid {
				t.Fatalf("failed/unknown CI was dropped, promoted or confused with review validity: %+v", authority)
			}
		})
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), identity.IssueID, identity.Candidate.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.State, row.Observation.MergeSHA = "merged", strings.Repeat("c", 40)
		row.Observation.TargetSHA, row.Observation.TargetContainsMerge = row.Observation.MergeSHA, true
		row.Observation.CI = nil
		return tx.PutDeliveryObservation(t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || !view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.Provider.CI != nil || view.DeliveryAuthorization.Candidate != identity.Candidate {
		t.Fatalf("completed original merge required invented new checks or changed its frozen candidate: %+v %v", view.DeliveryAuthorization, err)
	}
}

func TestDeliveryObservationKeepsInvalidReviewAndExcludesUnlinkedSource(t *testing.T) {
	h, as, review := automaticDeliveryObservationFixture(t)
	before, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		review.Revision++
		review.RequirementsDigest = strings.Repeat("d", 64)
		return tx.PutSharedDeliveryReview(t.Context(), review, "changed-review-requirements")
	}); err != nil {
		t.Fatal(err)
	}
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || view.DeliveryAuthorization.Valid || view.DeliveryAuthorization.SharedReview == nil || view.DeliveryAuthorization.SharedReview.Valid || view.DeliveryAuthorization.SharedReview.InvalidReason == "" || view.DeliveryAuthorization.SharedReview.ReviewID != review.ReviewID || view.DeliveryAuthorization.SharedReview.Round.AcceptanceID != review.Rounds[0].AcceptanceID {
		t.Fatalf("invalid original formal review was hidden or promoted: %+v %v", view.DeliveryAuthorization, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM shared_delivery_review_links WHERE review_id=? AND issue_id=? AND task_id=?`, review.ReviewID, before.DeliveryAuthorization.IssueID, before.DeliveryAuthorization.SourceTaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view, err = h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization == nil || view.DeliveryAuthorization.SharedReview != nil || view.DeliveryAuthorization.Valid {
		t.Fatalf("a PR-only match exposed a review outside this task's bound source: %+v %v", view.DeliveryAuthorization, err)
	}
}

func TestDeliveryObservationDoesNotCopySiblingAuthorization(t *testing.T) {
	h, delivery, _ := deliveryObservationFixture(t)
	view, err := h.app.ReadRunObservation(t.Context(), runPrincipal(delivery.RunID, delivery.Role))
	if err != nil {
		t.Fatal(err)
	}
	issue := canonicalTaskIssue(t, h, view.DeliveryAuthorization.IssueID)
	payload := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Observe without delivery authority", Spec: protocol.OwnerTaskSpec{Goal: "Read only the task's existing observation scope", Acceptance: []string{"Do not inherit another task's approval"}, Deliverables: []string{"report"}, Observation: &protocol.ObservationScope{WorkItemIDs: []string{issue.IssueID}}, Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	runtime := deliveryObservationRuntime(t, h)
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != created.TaskID {
		t.Fatalf("ordinary sibling claim: %+v %v", as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	view, err = h.app.ReadRunObservation(t.Context(), runPrincipal(as.RunID, as.Role))
	if err != nil || view.DeliveryAuthorization != nil || view.Deployment != nil {
		t.Fatalf("observation inherited sibling authority or deployment access: %+v %v", view, err)
	}
}

func deliveryObservationRuntime(t *testing.T, h *harness) *protocol.RunnerRuntime {
	t.Helper()
	var runtime protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		runtime = row.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", &runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return &runtime
}
