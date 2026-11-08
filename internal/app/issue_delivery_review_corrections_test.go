package app

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/reviewwatch"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDeliveryMergeOnlyCompletionRequiresCurrentIntegratedCandidate(t *testing.T) {
	for _, change := range []string{"valid", "target-rollback", "not-integrated", "base-drift", "advanced-target", "restored-snapshot", "running-base-drift"} {
		t.Run(change, func(t *testing.T) {
			h, i, source, c := deliveryFixture(t)
			// Keep GitHub's historical PR base distinct from its merged target.
			c.BaseSHA = "6683ad911185022de67e0ad6c070736afd1f1feb"
			c.TargetSHA = c.BaseSHA
			reportGitHubCandidate(t, h, i.IssueID, c, true)
			d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
			answerDeliveryForTest(t, h, d, "approve", "")
			op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var created taskResult
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			merged := c
			merged.ProviderState = "merged"
			merged.MergeSHA = "faa0888fce6975e19cba7fa853faec413de66181"
			merged.TargetSHA = merged.MergeSHA
			drift := merged
			contains := true
			switch change {
			case "target-rollback", "restored-snapshot":
				drift.TargetSHA, contains = c.BaseSHA, false
			case "not-integrated":
				contains = false
			case "base-drift", "running-base-drift":
				drift.BaseSHA = strings.Repeat("d", 40)
			case "advanced-target":
				drift.TargetSHA = strings.Repeat("e", 40)
			}
			// The running execution observes a valid merge before submitting its
			// artifact; the independent acceptance remains about that artifact.
			executeAcceptedDeliveryTaskForTest(t, h, created.TaskID, func() protocol.ArtifactPayload {
				reportGitHubCandidate(t, h, i.IssueID, merged, true)
				if change == "running-base-drift" {
					reportGitHubCandidate(t, h, i.IssueID, drift, contains)
				}
				artifact := testArtifact(h.task(created.TaskID).WorkspaceName)
				artifact.Report = &protocol.ResultReport{Body: "Verified merge of approved head; deployment was not authorized"}
				return artifact
			})
			acceptedTask := h.task(created.TaskID)
			if acceptedTask.Lifecycle != protocol.TaskCompleted || acceptedTask.CurrentArtifactID == "" {
				t.Fatal(acceptedTask)
			}
			if change != "valid" && change != "running-base-drift" {
				// Each helper call obtains a new generation/nonce and reports through
				// the actual provider report admission, including unchanged head/merge.
				reportGitHubCandidate(t, h, i.IssueID, drift, contains)
				if change == "restored-snapshot" {
					reportGitHubCandidate(t, h, i.IssueID, merged, true)
				}
			}
			before := deliveryDutyForTest(t, h, i.IssueID, source)
			acceptances, artifacts, runs := h.count("acceptances"), h.count("artifacts"), h.count("runs")
			op = updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Check current merge-only delivery endpoint after independent acceptance"}})
			if change == "valid" || change == "restored-snapshot" {
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				settled := deliveryDutyForTest(t, h, i.IssueID, source)
				if settled.Status != "complete" || len(settled.Evidence) != 2 || settled.Evidence[1].Observation == nil || !settled.Evidence[1].Observation.TargetContainsMerge || settled.Evidence[1].Observation.BaseSHA != c.BaseSHA {
					t.Fatal(settled)
				}
			} else {
				if before.Status != "executing" || before.Revision != d.Revision || before.ExecutionTaskID != created.TaskID {
					t.Fatal("observation rewrote the owner's original responsibility", before)
				}
				expectWorkCode(t, op, protocol.CodeStaleRevision)
				if got := deliveryDutyForTest(t, h, i.IssueID, source); !reflect.DeepEqual(got, before) {
					t.Fatal("rejected completion changed the original duty", before, got)
				}
				current := canonicalTaskIssue(t, h, i.IssueID)
				expectWorkCode(t, canonicalLifecycleControl(t, h, current, "close", canonicalLifecycleClosure(current, protocol.WorkItemClosureCompleted)), protocol.CodeInvalidTransition)
			}
			current := canonicalTaskIssue(t, h, i.IssueID)
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				facts, err := canonicalOwnerEndpointFacts(t.Context(), tx.Q(), current)
				if err != nil {
					return err
				}
				blocked := slices.ContainsFunc(facts.OpenAttention, func(b protocol.IssueBlocker) bool { return b.Code == "delivery_responsibility" && b.ObjectID == source })
				if blocked != (change != "valid" && change != "restored-snapshot") {
					t.Fatal("wrong durable close blocker", facts.OpenAttention)
				}
				passed, err := store.ListAcceptances(t.Context(), tx.Q(), created.TaskID, acceptedTask.CurrentArtifactID)
				if err == nil && !slices.ContainsFunc(passed, func(a store.AcceptanceRow) bool {
					return a.SpecVersion == acceptedTask.SpecVersion && a.Verdict == protocol.VerdictPassed
				}) {
					t.Fatal("lost independent acceptance", passed)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if h.task(created.TaskID).CurrentArtifactID != acceptedTask.CurrentArtifactID || h.task(source).Lifecycle != protocol.TaskCompleted || h.count("acceptances") != acceptances || h.count("artifacts") != artifacts || h.count("runs") != runs || h.count("issue_delivery_executions") != 1 || h.count("issue_delivery_duties") != 1 {
				t.Fatal("completion rewrote or revived delivery history")
			}
		})
	}
}

func TestDeliveryMergeOnlyCompletionRechecksPersistedFactsAndAuthorization(t *testing.T) {
	for _, change := range []string{"base", "target", "lineage", "revision", "owner-source"} {
		t.Run(change, func(t *testing.T) {
			h, i, source, c := deliveryFixture(t)
			d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
			answerDeliveryForTest(t, h, d, "approve", "")
			op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var created taskResult
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			acceptDeliveryTaskForTest(t, h, created.TaskID)
			// Historical persisted rows may predate candidate invalidation. A current
			// executing status alone is insufficient proof of integration or approval.
			c.TargetSHA = strings.Repeat("c", 40)
			observeDeliveryForTest(t, h, i.IssueID, c, "merged", c.TargetSHA)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch change {
				case "revision":
					duty, err := store.GetIssueDeliveryDuty(t.Context(), tx.Q(), i.IssueID, source)
					if err != nil {
						return err
					}
					duty.Revision++
					return tx.PutIssueDeliveryDuty(t.Context(), i.IssueID, duty)
				case "owner-source":
					_, err := tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
					return err
				}
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, c.DeliveryID)
				if err != nil {
					return err
				}
				switch change {
				case "base":
					row.Observation.BaseSHA = strings.Repeat("d", 40)
				case "target":
					row.Observation.TargetSHA = c.BaseSHA
				case "lineage":
					row.Observation.TargetContainsMerge = false
				}
				return tx.PutDeliveryObservation(t.Context(), row)
			}); err != nil {
				t.Fatal(err)
			}
			before := deliveryDutyForTest(t, h, i.IssueID, source)
			current := canonicalTaskIssue(t, h, i.IssueID)
			p := protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "Verify the current persisted endpoint, not a historical artifact"}}
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, p, rev(current.Revision)), UpdateIssueDelivery{})
			want := protocol.CodeStaleRevision
			if change == "owner-source" {
				want = protocol.CodeForbidden
			}
			if err != nil {
				if protocol.CodeOf(err) != want {
					t.Fatal(err)
				}
			} else {
				expectWorkCode(t, op, want)
			}
			if got := deliveryDutyForTest(t, h, i.IssueID, source); !reflect.DeepEqual(got, before) {
				t.Fatal("completion erased an unsettled historical duty", got)
			}
		})
	}
}

// Exercise provider parsing and the normal nonce/generation report admission.
func reportGitHubCandidate(t *testing.T, h *harness, issue string, c protocol.IssueDeliveryCandidate, ancestor bool) {
	t.Helper()
	reportGitHubCandidateOnHost(t, h, issue, c, ancestor, "linux-1")
}

func reportGitHubCandidateOnHost(t *testing.T, h *harness, issue string, c protocol.IssueDeliveryCandidate, ancestor bool, host string) {
	t.Helper()
	current := canonicalTaskIssue(t, h, issue)
	op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: issue}, protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, DeliveryID: c.DeliveryID}, rev(current.Revision)), IssueRefreshDelivery{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var rt protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetHostRuntime(t.Context(), tx.Q(), host)
		rt = r.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rt.Sequence++
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityDeliveryGitHub, protocol.CapabilityDeliveryCandidateIdentity, protocol.CapabilityDeliveryTargetLineage)
	if op := h.versionedHello(host, &rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignments := pollDelivery(t, h, host, rt.InstanceID)
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	client := reviewwatch.New(reviewwatch.Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[1] == "graphql" {
			return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 17, "url": c.URL, "state": strings.ToUpper(c.ProviderState), "baseRefOid": c.BaseSHA, "headRefOid": c.HeadSHA, "baseRef": map[string]any{"target": map[string]any{"oid": c.TargetSHA}}, "mergeCommit": map[string]any{"oid": c.MergeSHA}}}}})
		}
		status, base := "ahead", c.MergeSHA
		if c.TargetSHA == c.MergeSHA {
			status = "identical"
		}
		if !ancestor {
			status, base = "diverged", c.BaseSHA
		}
		return json.Marshal(map[string]any{"status": status, "merge_base_commit": map[string]any{"sha": base}})
	}})
	observed, err := client.Observe(t.Context(), reviewwatch.Ref{Provider: reviewwatch.GitHub, Repository: "example/repo", Number: 17})
	if err != nil {
		t.Fatal(err)
	}
	a := assignments[0]
	snapshot := protocol.DeliverySnapshot{State: observed.State, HeadSHA: observed.HeadSHA, BaseSHA: observed.BaseSHA, TargetSHA: observed.TargetSHA, TargetContainsMerge: observed.TargetContainsMerge, MergeSHA: observed.MergeSHA, URL: observed.URL}
	op = h.exec(runner(host), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: host}, protocol.DeliveryReportPayload{InstanceID: rt.InstanceID, WorkItemID: issue, DeliveryID: c.DeliveryID, Generation: a.Generation, Nonce: a.Nonce, Snapshot: &snapshot}, nil), ReportDelivery{})
	if !accepted(t, op) {
		t.Fatal(op)
	}
}

func TestDeliveryGitHubMergedSourceRejectsPreMergeBaseBuild(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "PR203-shape", true: "integrated-descendant"}[advanced], func(t *testing.T) {
			h, upgrade := upgradeHarness(t)
			_, i, source, c := deliveryFixtureOnHarness(t, h)
			c.ProviderState = "merged"
			c.BaseSHA = "6683ad911185022de67e0ad6c070736afd1f1feb"
			c.MergeSHA = "faa0888fce6975e19cba7fa853faec413de66181"
			c.HeadSHA = "d9de43492c08a956525fbc9758c65af8eba82f09"
			c.TargetSHA = c.MergeSHA
			if advanced {
				c.TargetSHA = strings.Repeat("e", 40)
			}
			c.Deploy = true
			c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
			c.DeploymentScope = "controlled self-update from current integrated target"

			reportGitHubCandidate(t, h, i.IssueID, c, true)
			d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
			answerDeliveryForTest(t, h, d, "approve", "")
			op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var created taskResult
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			// Check the final executable contract, not only the typed upgrade gate.
			// GitHub keeps the historical PR base distinct from the integrated target.
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				_, spec, err := loadTask(t.Context(), tx.Q(), created.TaskID)
				if err != nil {
					return err
				}
				contract := strings.Join(spec.Constraints, "\n")
				for _, required := range []string{"merge_sha is an ancestor of target_sha", "build the frozen target_sha", "target " + c.TargetSHA, "base " + c.BaseSHA} {
					if !strings.Contains(contract, required) {
						t.Errorf("merged delivery contract lacks %q: %s", required, contract)
					}
				}
				for _, forbidden := range []string{"merge_sha is an ancestor of base_sha", "build the approved current base_sha"} {
					if strings.Contains(contract, forbidden) {
						t.Errorf("merged delivery contract directs a pre-merge source: %q", forbidden)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			worker, ok, env := h.claim("linux-1")
			if !ok || worker.TaskID != created.TaskID {
				t.Fatal(worker, env)
			}
			h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			upgrade.DeliveryTaskID = created.TaskID
			upgrade.ArtifactSources = map[string]release.SourceIdentity{}
			for _, a := range upgrade.Manifest.Artifacts {
				upgrade.ArtifactSources[a.SHA256] = release.SourceIdentity{Commit: c.BaseSHA}
			}
			expectWorkCode(t, startTestUpgrade(h, "reject-pre-merge-base", upgrade), protocol.CodeInvalidRequest)
			if h.count("upgrades") != 0 {
				t.Fatal("pre-merge build installed")
			}
			upgrade.ArtifactSources = map[string]release.SourceIdentity{}
			for _, a := range upgrade.Manifest.Artifacts {
				upgrade.ArtifactSources[a.SHA256] = release.SourceIdentity{Commit: c.TargetSHA}
			}
			if op := startTestUpgrade(h, "correct-merged-source", upgrade); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}

func TestDeliveryOpenTargetDriftFencesAnswerTaskClaimAndStart(t *testing.T) {
	for _, boundary := range []string{"answer", "task", "claim", "start", "merge-base"} {
		t.Run(boundary, func(t *testing.T) {
			h, upgrade := upgradeHarness(t)
			_, i, source, c := deliveryFixtureOnHarness(t, h)
			c.Deploy = true
			c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
			c.DeploymentScope = "controlled target drift test"
			reportGitHubCandidate(t, h, i.IssueID, c, true)
			d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
			if boundary != "answer" {
				answerDeliveryForTest(t, h, d, "approve", "")
			}
			var task string
			if boundary == "claim" || boundary == "start" || boundary == "merge-base" {
				op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				var created taskResult
				if err := json.Unmarshal(op.Result, &created); err != nil {
					t.Fatal(err)
				}
				task = created.TaskID
			}
			if boundary == "start" || boundary == "merge-base" {
				worker, ok, env := h.claim("linux-1")
				if !ok || worker.TaskID != task {
					t.Fatal(worker, env)
				}
				if op := h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal("fixture did not start the approved execution", op)
				}
			}
			d = deliveryDutyForTest(t, h, i.IssueID, source)
			// baseRefOid and head stay fixed while the live target advances.
			c.TargetSHA = strings.Repeat("e", 40)
			if boundary == "merge-base" {
				c.ProviderState, c.MergeSHA = "merged", strings.Repeat("c", 40)
				c.BaseSHA, c.TargetSHA = strings.Repeat("e", 40), c.MergeSHA
			}
			if boundary == "start" || boundary == "merge-base" {
				// A separate observer avoids sending an empty Held set for the
				// live worker; this tests candidate admission, not a fake restart.
				reportGitHubCandidateOnHost(t, h, i.IssueID, c, true, "linux-2")
			} else {
				reportGitHubCandidate(t, h, i.IssueID, c, true)
			}
			expired := deliveryDutyForTest(t, h, i.IssueID, source)
			if expired.Status != d.Status || expired.Revision != d.Revision || expired.ExecutionTaskID != d.ExecutionTaskID || expired.AttentionID != d.AttentionID {
				t.Fatal("provider observation mechanically disposed of the owner's responsibility", expired)
			}
			switch boundary {
			case "answer":
				var a protocol.Attention
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					a, err = store.GetAttention(t.Context(), tx.Q(), d.AttentionID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				op := h.exec(owner, h.env("attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: "approve"}, rev(a.Revision)), AttentionCommand{Action: "respond"})
				if op.Status != protocol.OpSucceeded {
					t.Fatal("observation withdrew the original owner question", op)
				}
				expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeStaleRevision)
			case "task":
				expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeStaleRevision)
			case "claim":
				if worker, ok, _ := h.claim("linux-1"); ok {
					t.Fatal("drift acquired run", worker)
				}
			case "start", "merge-base":
				upgrade.DeliveryTaskID = task
				upgrade.ArtifactSources = map[string]release.SourceIdentity{upgrade.ServerHash: {Commit: c.TargetSHA}}
				expectWorkCode(t, startTestUpgrade(h, "drifted-start", upgrade), protocol.CodeStaleRevision)
			}
			if h.count("upgrades") != 0 {
				t.Fatal("drift installed release")
			}
		})
	}
}

func TestDeliveryDeferSurvivesPrepareProviderAndBaselineDrift(t *testing.T) {
	for _, explanation := range []string{"", "Wait until I explicitly resume this delivery"} {
		for _, drift := range []string{"validation", "provider", "baseline", "failure"} {
			t.Run(drift+"/text="+explanation, func(t *testing.T) {
				h, upgrade := upgradeHarness(t)
				_, i, source, c := deliveryFixtureOnHarness(t, h)
				if drift == "baseline" {
					c.Deploy = true
					c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
					c.DeploymentScope = "controlled deployment, deferred by owner"
				}
				reportGitHubCandidate(t, h, i.IssueID, c, true)
				d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
				answerDeliveryForTest(t, h, d, "defer", explanation)
				deferred := deliveryDutyForTest(t, h, i.IssueID, source)
				if deferred.Status != "deferred" || deferred.Revision <= d.Revision {
					t.Fatal("owner deferral was not persisted", deferred)
				}
				unchanged := prepareDeliveryForTest(t, h, i.IssueID, source, c)
				if unchanged.Status != "deferred" || unchanged.Revision != deferred.Revision || unchanged.AttentionID != d.AttentionID {
					t.Fatal("unchanged prepare erased owner deferral", unchanged)
				}
				c.Validation = "fresh checks on unchanged head"
				switch drift {
				case "provider":
					c.TargetSHA = strings.Repeat("e", 40)
					reportGitHubCandidate(t, h, i.IssueID, c, true)
				case "baseline":
					if op := startTestUpgrade(h, "another-installation", upgrade); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					batch := upgradeRead(h)
					op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					for _, host := range []string{"linux-1", "linux-2"} {
						upgradeAck(h, host, protocol.RunnerNormal)
					}
					reportGitHubCandidate(t, h, i.IssueID, c, true)
				case "failure":
					current := canonicalTaskIssue(t, h, i.IssueID)
					op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, DeliveryID: c.DeliveryID}, rev(current.Revision)), IssueRefreshDelivery{})
					if op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					var rt protocol.RunnerRuntime
					if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
						r, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
						rt = r.Runtime
						return err
					}); err != nil {
						t.Fatal(err)
					}
					a := pollDelivery(t, h, "linux-1", rt.InstanceID)
					if len(a) != 1 {
						t.Fatal(a)
					}
					if op := reportDelivery(t, h, "linux-1", rt.InstanceID, a[0], "", "timeout"); !accepted(t, op) {
						t.Fatal(op)
					}
				}
				// Both identical and changed prepare requests retain the original deferral.
				next := prepareDeliveryForTest(t, h, i.IssueID, source, c)
				if next.Status != "deferred" || next.AttentionID != d.AttentionID || h.count("attentions") != 1 || h.count("issue_delivery_executions") != 0 {
					t.Fatal(next)
				}
				expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, next), protocol.CodeStaleRevision)
				reportGitHubCandidate(t, h, i.IssueID, c, true)
				// This is a new explicit owner command, not an agent's prepare operation.
				op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "resume", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Owner explicitly resumes after current validation"}})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				resumed := deliveryDutyForTest(t, h, i.IssueID, source)
				if (resumed.Status != "awaiting_owner" && (resumed.Status != "preparing" || resumed.PolicyAttentionID == "")) || resumed.AttentionID == d.AttentionID || h.count("attentions") != 2 {
					t.Fatal(resumed)
				}
				expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, resumed), historicalOrPolicyReadinessCode(resumed))
				if h.task(source).Lifecycle != protocol.TaskCompleted {
					t.Fatal("original completed task changed")
				}
			})
		}
	}
}

func TestDeliveryDeferredManagerNeedsCurrentOwnerResumeSource(t *testing.T) {
	h := hostedManagerHarness(t)
	_, i, source, c := deliveryFixtureOnHarness(t, h)
	i = assignHostedManager(t, h, i)
	old := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + i.IssueID, Body: "Earlier permission to prepare delivery, before the later deferral"}, nil), SubmitMessage{})
	if old.Status != protocol.OpAccepted {
		t.Fatal(old)
	}
	var historical struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(old.Result, &historical); err != nil {
		t.Fatal(err)
	}

	earlier, _, ready, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+i.IssueID)
	if err != nil || !ready {
		t.Fatal(ready, err)
	}
	earlierTurn := startHostedReview(t, h, "issue:"+i.IssueID, earlier)
	earlierManager := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + i.IssueID, Thread: earlierTurn.ThreadID, ConversationID: "issue:" + i.IssueID}
	if op := hostedEffect(t, h, earlierManager, earlierTurn, 1, protocol.Target{Type: "issue", ID: i.IssueID}, nil, hostedReviewPayload(t, h, i), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, earlierManager, earlierTurn, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, earlierTurn)
	i = canonicalTaskIssue(t, h, i.IssueID)

	d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
	answerDeliveryForTest(t, h, d, "defer", "Wait until I explicitly resume this delivery")
	var deferMessage string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT d.answered_message_id FROM decisions d JOIN attentions a USING(decision_id) WHERE a.attention_id=?`, d.AttentionID).Scan(&deferMessage)
	}); err != nil {
		t.Fatal(err)
	}
	c.Validation = "new validation must not erase owner deferral"
	prepareDeliveryForTest(t, h, i.IssueID, source, c)
	submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + i.IssueID, Body: "Resume this deferred delivery and present a current candidate for confirmation"}, nil), SubmitMessage{})
	if submitted.Status != protocol.OpAccepted {
		t.Fatal(submitted)
	}
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submitted.Result, &message); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+i.IssueID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, "issue:"+i.IssueID, delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + i.IssueID, Thread: turn.ThreadID, ConversationID: "issue:" + i.IssueID}
	i = canonicalTaskIssue(t, h, i.IssueID)
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: i.IssueID}, nil, hostedReviewPayload(t, h, i), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i = canonicalTaskIssue(t, h, i.IssueID)
	p := protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "resume", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message.MessageID, ExpectedSourceRevision: rev(1)}, ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Relay the current actual owner request to resume confirmation"}}
	for _, bad := range []string{"missing", "original-defer", "historical", "withdrawn", "edited"} {
		t.Run(bad, func(t *testing.T) {
			payload := p
			if bad == "missing" {
				payload.SourceMessageID = ""
				payload.ExpectedSourceRevision = nil
			}
			if bad == "original-defer" {
				payload.SourceMessageID = deferMessage
			}
			if bad == "historical" {
				payload.SourceMessageID = historical.MessageID
			}
			// The rejected transaction rolls back controls, preserving the real source
			// for the following valid hosted relay.
			rejected := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if bad == "withdrawn" {
					if _, err := tx.Exec(t.Context(), `INSERT INTO conversation_message_controls(message_id,withdrawn_at) VALUES(?,1) ON CONFLICT(message_id) DO UPDATE SET withdrawn_at=1`, message.MessageID); err != nil {
						return err
					}
				}
				if bad == "edited" {
					if _, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET content_revision=content_revision+1 WHERE message_id=?`, message.MessageID); err != nil {
						return err
					}
				}
				_, err := (UpdateIssueDelivery{}).Apply(t.Context(), &Exec{Principal: principal, Env: h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, payload, rev(i.Revision)), Now: h.clock.Now(), IDs: h.ids}, tx)
				return err
			})
			want := map[string]protocol.Code{"missing": protocol.CodeForbidden, "original-defer": protocol.CodeForbidden, "historical": protocol.CodeForbidden, "withdrawn": protocol.CodeInvalidTransition, "edited": protocol.CodeStaleRevision}[bad]
			if protocol.CodeOf(rejected) != want {
				t.Fatal(bad, rejected)
			}
			if got := deliveryDutyForTest(t, h, i.IssueID, source); got.Status != "deferred" || h.count("attentions") != 1 {
				t.Fatal(got)
			}
		})
	}
	op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: i.IssueID}, rev(i.Revision), p, UpdateIssueDelivery{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resumed := deliveryDutyForTest(t, h, i.IssueID, source)
	if (resumed.Status != "awaiting_owner" && (resumed.Status != "preparing" || resumed.PolicyAttentionID == "")) || resumed.AttentionID == d.AttentionID || h.count("attentions") != 2 {
		t.Fatal(resumed)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, resumed), historicalOrPolicyReadinessCode(resumed))
}

func TestDeliveryMergedCandidateRequiresCurrentIntegratedTarget(t *testing.T) {
	h, i, source, c := deliveryFixture(t)
	c.ProviderState = "merged"
	c.MergeSHA = strings.Repeat("c", 40)
	c.TargetSHA = strings.Repeat("d", 40)
	c.Deploy = true
	c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	c.DeploymentScope = "current integrated target only"
	reportGitHubCandidate(t, h, i.IssueID, c, false)
	expectWorkCode(t, updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "target does not contain merge"}}), protocol.CodeStaleRevision)
	reportGitHubCandidate(t, h, i.IssueID, c, true)
	d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
	answerDeliveryForTest(t, h, d, "approve", "")
	c.TargetSHA = strings.Repeat("e", 40)
	reportGitHubCandidate(t, h, i.IssueID, c, true)
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeStaleRevision)
}
