package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// All three Tasks finish their real implementation/independent acceptance paths.
// Only the updater installation facts use the existing controlled test double.
func contributionSettlementFixture(t *testing.T, deploy bool) (*harness, protocol.Issue, []string, string, store.UpgradeRow) {
	t.Helper()
	h, runtime, issue, impl, candidate := liveReviewedDeliveryFixture(t)
	var source string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT task_id FROM shared_delivery_review_links WHERE issue_id=? AND task_id<>? AND task_id<>'' LIMIT 1`, issue.IssueID, impl.TaskID).Scan(&source)
	}); err != nil {
		t.Fatal(err)
	}
	// The second accepted contribution prepares the candidate without executing
	// delivery. A third Task later delivers the whole shared PR.
	intermediate := impl.TaskID
	result := testArtifact(h.task(intermediate).WorkspaceName)
	result.Mounts[0].Changed, result.Mounts[0].CommitSHA = true, candidate.HeadSHA
	completeContributionTaskForTest(t, h, runtime, impl, result)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	raw, err := json.Marshal(h.storedSpec(intermediate, 1))
	if err != nil {
		t.Fatal(err)
	}
	var spec protocol.OwnerTaskSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	created := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{Title: "Execute the complete shared PR", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var final taskResult
	if err := json.Unmarshal(created.Result, &final); err != nil {
		t.Fatal(err)
	}
	var ok bool
	impl, ok = h.claimInputRun(runtime)
	if !ok || impl.TaskID != final.TaskID {
		t.Fatal("final execution was not claimed", impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: candidate.DeliveryID, TaskID: impl.TaskID})
	if deploy {
		candidate.Deploy = true
		candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
		candidate.DeploymentScope = "Install the merged whole PR on the original server and host"
	}
	duty := prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, candidate)
	if deploy {
		answerDeliveryForTest(t, h, duty, "approve", "")
	}
	op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), Action: "bind", IssueControlPayload: protocol.IssueControlPayload{Reason: "Execute the complete reviewed PR in this original Task"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	merge := strings.Repeat("c", 40)
	observation := candidate
	observation.TargetSHA = merge
	observeDeliveryForTest(t, h, issue.IssueID, observation, "merged", merge)
	artifact := testArtifact(h.task(impl.TaskID).WorkspaceName)
	artifact.Mounts[0].Changed, artifact.Mounts[0].CommitSHA = true, candidate.HeadSHA
	artifact.Report = &protocol.ResultReport{Body: "The complete reviewed PR was merged and its original delivery verified"}
	var batch store.UpgradeRow
	if deploy {
		hash := strings.Repeat("e", 64)
		batch = store.UpgradeRow{DeliveryTaskID: impl.TaskID, View: protocol.UpgradeView{UpgradeID: "original-complete-installation", ReleaseID: "verified-merge", SourceCommit: merge, Revision: 22, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "verified-merge"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", ServerProcess: "original-process", ServerHealthy: 2, Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
			t.Fatal(err)
		}
		artifact.Report.Evidence = []string{"upgrade:" + batch.View.UpgradeID}
	}
	completeContributionTaskForTest(t, h, runtime, impl, artifact)
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: impl.TaskID, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the actual whole PR execution"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, canonicalTaskIssue(t, h, issue.IssueID), []string{source, intermediate}, impl.TaskID, batch
}

func completeContributionTaskForTest(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, impl protocol.Assignment, artifact protocol.ArtifactPayload) {
	t.Helper()
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.TaskID != impl.TaskID || accept.Role != protocol.RoleAccept {
		t.Fatal("original independent acceptance", accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(impl.TaskID)
	if !h.releaseResource(impl.TaskID, resource.WorkspaceID) {
		t.Fatal("release accepted Task")
	}
}

func TestContributionDeliverySettlementClosesIssueFromCompletedSharedPRExecution(t *testing.T) {
	for _, deploy := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "deployment"}[deploy], func(t *testing.T) {
			h, issue, sources, execution, batch := contributionSettlementFixture(t, deploy)
			source := sources[0]
			original := deliveryDutyForTest(t, h, issue.IssueID, source)
			settled := deliveryDutyForTest(t, h, issue.IssueID, execution)
			if original.Status != "preparing" || original.Candidate != nil || original.ExecutionTaskID != "" || settled.Status != "complete" {
				t.Fatal("not the original unbound contribution", original, settled)
			}
			if deploy {
				advanceHistoricalDeploymentFacts(t, h, issue.IssueID, execution, batch, false)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			tasks, executions := h.count("tasks"), h.count("issue_delivery_executions")
			p := protocol.UpdateIssueDeliveryPayload{TaskID: source, ExecutionTaskID: execution, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Settle this accepted contribution through the completed whole PR execution"}}
			env := h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
			op := h.exec(owner, env, UpdateIssueDelivery{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if replay := h.exec(owner, env, UpdateIssueDelivery{}); !reflect.DeepEqual(op, replay) {
				t.Fatal("historical settlement was replayed as new work", replay)
			}
			done := deliveryDutyForTest(t, h, issue.IssueID, source)
			if done.Status != "complete" || done.ExecutionTaskID != execution || done.Candidate != nil || done.Authorization != nil || done.AcceptanceID != original.AcceptanceID || done.ArtifactID != original.ArtifactID || len(done.Evidence) < 3 {
				t.Fatal("contribution lost its original facts", done)
			}
			if !reflect.DeepEqual(deliveryDutyForTest(t, h, issue.IssueID, execution), settled) || h.count("tasks") != tasks || h.count("issue_delivery_executions") != executions {
				t.Fatal("historical settlement changed the completed execution")
			}
			if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: sources[1], ExecutionTaskID: execution, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the second accepted contribution through the same original execution"}}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var acknowledgements []protocol.CompletionReviewAcknowledgement
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				v, err := views.CanonicalIssue(t.Context(), tx.Q(), issue.IssueID, 0)
				for _, review := range v.PendingReviews {
					acknowledgements = append(acknowledgements, protocol.CompletionReviewAcknowledgement{EventID: review.EventID, Reason: "Review both actual completed Task endpoints"})
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			if len(acknowledgements) > 0 {
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "review_completions", map[string]any{"expected_assignment_epoch": issue.AssignmentEpoch, "reviews": acknowledgements}))
			}
			closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)))
			if closed.State != protocol.IssueClosed {
				t.Fatal("completed contribution still blocks closure", closed)
			}
		})
	}
}

func TestContributionDeliverySettlementRejectsUnrelatedOrUnsettledExecution(t *testing.T) {
	for _, scenario := range []string{"missing_link", "different_pr", "cross_issue", "no_execution", "unaccepted", "unsettled", "existing_candidate", "existing_execution"} {
		t.Run(scenario, func(t *testing.T) {
			h, issue, sources, execution, _ := contributionSettlementFixture(t, false)
			source := sources[0]
			p := protocol.UpdateIssueDeliveryPayload{TaskID: source, ExecutionTaskID: execution, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle only an actual accepted contribution to a completed shared PR"}}
			if scenario == "cross_issue" {
				other := createCanonicalOwnerTestIssue(t, h)
				p.ExecutionTaskID = createCanonicalTaskForTest(t, h, other)
			}
			if scenario == "no_execution" {
				p.ExecutionTaskID = sources[1]
			}
			// Each variant removes one real completed fixture premise. No variant
			// manufactures the successful execution or its acceptance evidence.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch scenario {
				case "missing_link":
					_, err := tx.Exec(t.Context(), `DELETE FROM shared_delivery_review_links WHERE issue_id=? AND task_id=?`, issue.IssueID, source)
					return err
				case "different_pr":
					if err := tx.PutSharedDeliveryReview(t.Context(), protocol.SharedDeliveryReview{ReviewID: "other-pr-review", Provider: "github", Repository: "example/repo", Number: 18, Revision: 1}, "other-pr-fact"); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE shared_delivery_review_links SET review_id=? WHERE issue_id=? AND task_id=?`, "other-pr-review", issue.IssueID, source)
					return err
				case "unaccepted":
					_, err := tx.Exec(t.Context(), `UPDATE acceptances SET verdict='failed' WHERE task_id=?`, execution)
					return err
				case "unsettled":
					d, err := store.GetIssueDeliveryDuty(t.Context(), tx.Q(), issue.IssueID, execution)
					if err != nil {
						return err
					}
					d.Status = "executing"
					return tx.PutIssueDeliveryDuty(t.Context(), issue.IssueID, d)
				case "existing_candidate", "existing_execution":
					d, err := store.GetIssueDeliveryDuty(t.Context(), tx.Q(), issue.IssueID, source)
					if err != nil {
						return err
					}
					if scenario == "existing_candidate" {
						settled, err := store.GetIssueDeliveryDuty(t.Context(), tx.Q(), issue.IssueID, execution)
						if err != nil {
							return err
						}
						d.Candidate = settled.Candidate
					} else {
						d.ExecutionTaskID = sources[1]
					}
					return tx.PutIssueDeliveryDuty(t.Context(), issue.IssueID, d)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := deliveryDutyForTest(t, h, issue.IssueID, source)
			revision := canonicalTaskIssue(t, h, issue.IssueID).Revision
			if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpFailed {
				t.Fatal("invalid historical settlement was accepted", op)
			}
			if after := deliveryDutyForTest(t, h, issue.IssueID, source); !reflect.DeepEqual(after, before) || canonicalTaskIssue(t, h, issue.IssueID).Revision != revision {
				t.Fatal("rejected settlement changed original responsibility", after)
			}
		})
	}
}

func TestContributionDeliverySettlementRetainsOriginalUpgrade(t *testing.T) {
	h, issue, sources, execution, batch := contributionSettlementFixture(t, true)
	before := deliveryDutyForTest(t, h, issue.IssueID, sources[0])
	p := protocol.UpdateIssueDeliveryPayload{TaskID: sources[0], ExecutionTaskID: execution, Action: "complete", UpgradeID: "unrelated-installation", IssueControlPayload: protocol.IssueControlPayload{Reason: "Reference the actual original installation"}}
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("unrelated upgrade was accepted", op)
	}
	if after := deliveryDutyForTest(t, h, issue.IssueID, sources[0]); !reflect.DeepEqual(before, after) {
		t.Fatal(after)
	}
	p.UpgradeID = batch.View.UpgradeID
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	done := deliveryDutyForTest(t, h, issue.IssueID, sources[0])
	settled := deliveryDutyForTest(t, h, issue.IssueID, execution)
	if !reflect.DeepEqual(done.Evidence[2:], settled.Evidence) || done.Authorization != nil || done.Candidate != nil {
		t.Fatal("settlement changed frozen proof or copied execution authority", done)
	}
}
