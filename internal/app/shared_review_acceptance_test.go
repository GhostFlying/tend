package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func acceptanceSharedReviewFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment, protocol.IssueDeliveryCandidate) {
	t.Helper()
	h, rt, issue, _, candidate, _ := sharedReviewFixture(t)
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityWorkerObservation)
	rt.Sequence++
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := protocol.OwnerTaskSpec{Goal: "Deliver the complete shared PR", Acceptance: []string{"Independent whole PR review"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}, Observation: &protocol.ObservationScope{WorkItemIDs: []string{issue.IssueID}}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{Title: "Whole PR", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	impl, ok := h.claimInputRun(rt)
	if !ok || impl.TaskID != created.TaskID {
		t.Fatal(impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifact := testArtifact(h.task(created.TaskID).WorkspaceName)
	artifact.Mounts[0].CommitSHA, artifact.Mounts[0].Changed = candidate.HeadSHA, true
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(rt)
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatal(accept)
	}
	if op := h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: created.TaskID}))
	return h, rt, canonicalTaskIssue(t, h, issue.IssueID), accept, candidate
}

func acceptanceSharedReviewEvidence(r protocol.SharedDeliveryReview, findings []protocol.SharedReviewFinding) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"delivery_review": protocol.SharedReviewEvidence{Provider: r.Provider, Repository: r.Repository, Number: r.Number, HeadSHA: r.HeadSHA, BaseSHA: r.BaseSHA, RequirementsDigest: r.RequirementsDigest, RepositoryCheck: "Reviewed entire PR and repository checks under its convention", Findings: findings}})
	return raw
}

func TestAcceptanceSharedReviewPrebindUsesActualRoleAndVerdict(t *testing.T) {
	for _, verdict := range []protocol.Verdict{protocol.VerdictPassed, protocol.VerdictChangesRequired, protocol.VerdictBlocked, protocol.VerdictInconclusive} {
		t.Run(string(verdict), func(t *testing.T) {
			h, _, issue, accept, _ := acceptanceSharedReviewFixture(t)
			pending, err := h.app.ReadRunObservation(t.Context(), runPrincipal(accept.RunID, accept.Role))
			if err != nil || len(pending.SharedReviews) != 1 || pending.SharedReviews[0].Round != nil || pending.SharedReviews[0].Status != "pending" {
				t.Fatal(pending, err)
			}
			r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
			round := r.Rounds[0]
			if round.ReviewerRole != protocol.RoleAccept || round.Reviewer != "linux-1/beta" || round.ReviewerProfile == nil || round.ArtifactID == "" || round.RunID != accept.RunID {
				t.Fatal(r)
			}
			observed, err := h.app.ReadRunObservation(t.Context(), runPrincipal(accept.RunID, accept.Role))
			if err != nil || len(observed.SharedReviews) != 1 || observed.SharedReviews[0].Round.ReviewerRole != protocol.RoleAccept || observed.SharedReviews[0].Round.ArtifactID != round.ArtifactID || observed.SharedReviews[0].RoundLimit != 0 || observed.SharedReviews[0].SuggestedRounds != 2 {
				t.Fatal(observed, err)
			}
			before := h.count("shared_delivery_review_history")
			if op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
				t.Fatal("preflight became verdict", op)
			}
			if h.count("shared_delivery_review_history") != before {
				t.Fatal("missing verdict changed review")
			}
			findings := []protocol.SharedReviewFinding(nil)
			if verdict == protocol.VerdictChangesRequired {
				findings = []protocol.SharedReviewFinding{{ID: "real-finding", Priority: "P2", Body: "Fix the actual candidate's omitted case"}}
			}
			if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: verdict, Evidence: acceptanceSharedReviewEvidence(r, findings)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			last := r.Rounds[0]
			if last.AcceptanceID == "" || last.AcceptanceVerdict != verdict || last.RunID != accept.RunID {
				t.Fatal("actual immutable provenance lost", r)
			}
			want := "waiting"
			if verdict == protocol.VerdictPassed {
				want = "passed"
			}
			if verdict == protocol.VerdictChangesRequired {
				want = "changes_requested"
				if len(last.Result.Findings) != 1 || last.Result.Findings[0].ID != "real-finding" {
					t.Fatal(r)
				}
			}
			if r.Status != want || r.Valid != (verdict == protocol.VerdictPassed) {
				t.Fatal("actual verdict promoted or stranded", r)
			}
		})
	}
}

func TestAcceptanceSharedReviewSameTaskFocusedRecheckNeedsNewArtifactAndRun(t *testing.T) {
	h, rt, issue, accept, candidate := acceptanceSharedReviewFixture(t)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	findings := []protocol.SharedReviewFinding{{ID: "defect", Body: "Actual initial defect"}}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Evidence: acceptanceSharedReviewEvidence(r, findings), Issues: json.RawMessage(`["Actual initial defect"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "defect", Action: "fixed", Reason: "Fix in the original task"}}}))
	if op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}); op.Status == protocol.OpSucceeded {
		t.Fatal("old artifact/run counted as new review", op)
	}
	impl, ok := h.claimInputRun(rt)
	if !ok || impl.TaskID != accept.TaskID || impl.Role != protocol.RoleImplement {
		t.Fatal("original task did not get real remediation", impl)
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	artifact := testArtifact(h.task(accept.TaskID).WorkspaceName)
	candidate.HeadSHA = strings.Repeat("d", 40)
	artifact.Mounts[0].CommitSHA, artifact.Mounts[0].Changed = candidate.HeadSHA, true
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
	second, ok := h.claimInputRun(rt)
	if !ok || second.Role != protocol.RoleAccept || second.TaskID != accept.TaskID {
		t.Fatal(second)
	}
	h.runAs(second, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	if len(r.Rounds) != 2 || r.Rounds[0].ArtifactID == r.Rounds[1].ArtifactID || r.Rounds[0].RunID == r.Rounds[1].RunID || r.Rounds[1].Reviewer != r.Rounds[0].Reviewer {
		t.Fatal(r)
	}
	if op := h.runAs(second, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: acceptanceSharedReviewEvidence(r, nil)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || r.Status != "passed" || len(r.Rounds) != 2 || r.Rounds[0].Result.Findings[0].ID != "defect" {
		t.Fatal(r)
	}
}

func TestAcceptanceSharedReviewProjectionRejectsOtherTaskSpecArtifactAndScope(t *testing.T) {
	h, _, issue, accept, _ := acceptanceSharedReviewFixture(t)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	for _, c := range []struct {
		task     string
		spec     int64
		artifact string
		scope    []string
		count    int
	}{{accept.TaskID, 1, r.Rounds[0].ArtifactID, []string{issue.IssueID}, 1}, {"other-task", 1, r.Rounds[0].ArtifactID, []string{issue.IssueID}, 0}, {accept.TaskID, 2, r.Rounds[0].ArtifactID, []string{issue.IssueID}, 0}, {accept.TaskID, 1, "other-artifact", []string{issue.IssueID}, 0}, {accept.TaskID, 1, r.Rounds[0].ArtifactID, nil, 0}} {
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			v, err := views.AcceptanceSharedReviews(t.Context(), tx.Q(), c.task, c.spec, c.artifact, c.scope)
			if err == nil && len(v) != c.count {
				t.Fatal("scope expanded", c, v)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAcceptanceSharedReviewKeepsNarrowVerdictAndOriginalReviewerSupplement(t *testing.T) {
	h, rt, issue, accept, _ := acceptanceSharedReviewFixture(t)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"unit_test":"passed; no whole PR scope established"}`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(accept.TaskID)
	if !h.releaseResource(accept.TaskID, resource.WorkspaceID) {
		t.Fatal("release")
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if r.Valid || r.Status != "reviewing" || r.Rounds[0].AcceptanceID == "" || r.Rounds[0].AcceptanceVerdict != protocol.VerdictPassed || r.Rounds[0].Result != nil {
		t.Fatal("narrow verdict promoted or lost", r)
	}
	original := r.Rounds[0].AcceptanceID
	before := h.count("shared_delivery_review_history")
	same := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if same.Revision != r.Revision || h.count("shared_delivery_review_history") != before {
		t.Fatal("same unresolved immutable evidence created another wakeup", same)
	}
	setPRPolicyForTest(t, h, issue.IssueID, "manual")
	supplement := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "supplement", DeliveryID: "pr", ReviewerTaskID: supplement}))
	executeSharedReviewForTest(t, h, rt, r, supplement, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || len(r.Rounds) != 1 || r.Rounds[0].AcceptanceID != original || r.Rounds[0].SupplementTaskID != supplement || r.Rounds[0].SupplementRunID == "" {
		t.Fatal("legitimate same-round evidence recovery lost", r)
	}
}

func TestAcceptanceSharedReviewRejectsWrongRoleAndCandidateBeforeSpendingRound(t *testing.T) {
	for _, change := range []string{"role", "candidate"} {
		t.Run(change, func(t *testing.T) {
			h, _, issue, accept, candidate := acceptanceSharedReviewFixture(t)
			p := protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}
			if change == "role" {
				p.ReviewerRole = protocol.RoleImplement
			} else {
				candidate.HeadSHA = strings.Repeat("c", 40)
				observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
			}
			before := h.count("shared_delivery_review_history")
			if op := sharedReviewAction(t, h, issue.IssueID, p); op.Status == protocol.OpSucceeded {
				t.Fatal("invalid binding accepted", op)
			}
			if h.count("shared_delivery_review_history") != before {
				t.Fatal("rejected binding consumed a round")
			}
		})
	}
}

func TestAcceptanceSharedReviewRetainsStaleRequirementsAndActualProfileFacts(t *testing.T) {
	for _, change := range []string{"requirements", "profile"} {
		t.Run(change, func(t *testing.T) {
			h, _, issue, accept, _ := acceptanceSharedReviewFixture(t)
			r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
			if change == "profile" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE run_agent_executions SET pin_json=json_set(pin_json,'$.profile.model','actual-different-model') WHERE run_id=?`, accept.RunID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: acceptanceSharedReviewEvidence(r, nil)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if change == "requirements" {
				issue = canonicalTaskIssue(t, h, issue.IssueID)
				issue.Plan.Scope = "An actual new requirement after this reviewer started"
				canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if r.Valid || r.Status != "passed" || r.Rounds[0].Result.Verdict != "passed" || r.Rounds[0].RunID != accept.RunID || r.InvalidReason == "" {
				t.Fatal("old/invalid execution facts discarded or granted current approval", r)
			}
		})
	}
}

func TestAcceptanceSharedReviewWaitingRecheckDoesNotReplayOldVerdict(t *testing.T) {
	h, rt, issue, accept, _ := acceptanceSharedReviewFixture(t)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked, Evidence: json.RawMessage(`{"prerequisite":"external evidence unavailable"}`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if r.Status != "waiting" {
		t.Fatal(r)
	}
	if op := h.requestAction(accept.TaskID, protocol.ActionAccept, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(rt)
	if !ok || next.Role != protocol.RoleAccept || next.TaskID != accept.TaskID {
		t.Fatal(next)
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	if len(r.Rounds) != 2 || r.Rounds[0].ArtifactID != r.Rounds[1].ArtifactID || r.Rounds[0].RunID == r.Rounds[1].RunID {
		t.Fatal("not a new real acceptance attempt", r)
	}
	before := h.count("shared_delivery_review_history")
	if op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("old blocked verdict replayed into new round", op)
	}
	if h.count("shared_delivery_review_history") != before {
		t.Fatal("new round consumed old verdict")
	}
	if op := h.runAs(next, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: acceptanceSharedReviewEvidence(r, nil)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || r.Rounds[1].RunID != next.RunID || r.Rounds[1].AcceptanceID == r.Rounds[0].AcceptanceID || r.Rounds[0].AcceptanceVerdict != protocol.VerdictBlocked {
		t.Fatal("new actual evidence or old waiting history lost", r)
	}
}

func TestAcceptanceSharedReviewBindingNotHiddenByUnrelatedHistory(t *testing.T) {
	h, _, issue, accept, _ := acceptanceSharedReviewFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for n := 0; n < 101; n++ {
			id := fmt.Sprintf("review-%03d", n)
			r := protocol.SharedDeliveryReview{ReviewID: id, Provider: "github", Repository: "example/other", Number: int64(n + 1), Revision: 1, Status: "pending"}
			if err := tx.PutSharedDeliveryReview(t.Context(), r, id); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO shared_delivery_review_links(review_id,issue_id,delivery_id,task_id) VALUES(?,?,?,'')`, id, issue.IssueID, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	observation, err := h.app.ReadRunObservation(t.Context(), runPrincipal(accept.RunID, accept.Role))
	if err != nil || len(observation.SharedReviews) != 1 || observation.SharedReviews[0].Repository != "example/repo" || observation.SharedReviews[0].RequirementsDigest == "" {
		t.Fatal("current task's binding disappeared behind unrelated Issue history", observation.SharedReviews, err)
	}
}

func TestAcceptanceSharedReviewResponsibleContinuationRetainsSameArtifactWaitingReview(t *testing.T) {
	h, rt, issue, accept, _ := acceptanceSharedReviewFixture(t)
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
	for n := 0; n < 2; n++ {
		if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked, Evidence: json.RawMessage(`{"prerequisite":"repository evidence is temporarily unavailable"}`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
		if n == 0 {
			if op := h.requestAction(accept.TaskID, protocol.ActionAccept, 2); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			next, ok := h.claimInputRun(rt)
			if !ok || next.Role != protocol.RoleAccept {
				t.Fatal(next)
			}
			accept = next
			h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: accept.TaskID, ReviewerRole: protocol.RoleAccept}))
		}
	}
	if r.Status != "waiting" || len(r.Rounds) != 2 || len(r.BudgetAuthorizations) != 0 {
		t.Fatal("waiting evidence became a human review-budget gate", r)
	}
	if op := h.requestAction(accept.TaskID, protocol.ActionAccept, 3); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(rt)
	if !ok || next.Role != protocol.RoleAccept {
		t.Fatal(next)
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: next.TaskID, ReviewerRole: protocol.RoleAccept}))
	if len(r.Rounds) != 3 || r.Rounds[2].ArtifactID != r.Rounds[0].ArtifactID || r.Rounds[2].RunID == r.Rounds[0].RunID || r.Rounds[2].RunID == r.Rounds[1].RunID {
		t.Fatal("responsible continuation lost same-artifact waiting provenance", r)
	}
	if op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}); op.Status == protocol.OpSucceeded {
		t.Fatal("prior waiting verdict counted as third result", op)
	}
	if op := h.runAs(next, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: acceptanceSharedReviewEvidence(r, nil)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || r.Status != "passed" || len(r.Rounds) != 3 || len(r.BudgetAuthorizations) != 0 || r.Rounds[2].RunID != next.RunID || r.Rounds[0].AcceptanceVerdict != protocol.VerdictBlocked || r.Rounds[1].AcceptanceVerdict != protocol.VerdictBlocked {
		t.Fatal("responsible continuation did not preserve real waiting history and costs", r)
	}
}
