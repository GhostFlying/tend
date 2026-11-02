package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/reviewwatch"
	"github.com/GhostFlying/tend/internal/store"
)

// Exercise provider parsing and the normal nonce/generation report admission.
func reportGitHubCandidate(t *testing.T, h *harness, issue string, c protocol.IssueDeliveryCandidate, ancestor bool) {
	t.Helper()
	current := canonicalTaskIssue(t, h, issue)
	op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: issue}, protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, DeliveryID: c.DeliveryID}, rev(current.Revision)), IssueRefreshDelivery{})
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
	rt.Sequence++
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityDeliveryGitHub, protocol.CapabilityDeliveryCandidateIdentity, protocol.CapabilityDeliveryTargetLineage)
	if op := h.versionedHello("linux-1", &rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignments := pollDelivery(t, h, "linux-1", rt.InstanceID)
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
	op = h.exec(runner("linux-1"), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: "linux-1"}, protocol.DeliveryReportPayload{InstanceID: rt.InstanceID, WorkItemID: issue, DeliveryID: c.DeliveryID, Generation: a.Generation, Nonce: a.Nonce, Snapshot: &snapshot}, nil), ReportDelivery{})
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
	for _, boundary := range []string{"answer", "task", "claim", "start"} {
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
			if boundary == "claim" || boundary == "start" {
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
			if boundary == "start" {
				worker, ok, env := h.claim("linux-1")
				if !ok || worker.TaskID != task {
					t.Fatal(worker, env)
				}
				h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			}
			// baseRefOid and head stay fixed while the live target advances.
			c.TargetSHA = strings.Repeat("e", 40)
			reportGitHubCandidate(t, h, i.IssueID, c, true)
			expired := deliveryDutyForTest(t, h, i.IssueID, source)
			if expired.Status != "preparing" || expired.Revision <= d.Revision {
				t.Fatal(expired)
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
				if op.Status == protocol.OpSucceeded {
					t.Fatal("expired question answered", op)
				}
			case "task":
				expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, d), protocol.CodeStaleRevision)
			case "claim":
				if worker, ok, _ := h.claim("linux-1"); ok {
					t.Fatal("drift acquired run", worker)
				}
			case "start":
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
	for _, drift := range []string{"validation", "provider", "baseline", "failure"} {
		t.Run(drift, func(t *testing.T) {
			h, upgrade := upgradeHarness(t)
			_, i, source, c := deliveryFixtureOnHarness(t, h)
			if drift == "baseline" {
				c.Deploy = true
				c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
				c.DeploymentScope = "controlled deployment, deferred by owner"
			}
			reportGitHubCandidate(t, h, i.IssueID, c, true)
			d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
			answerDeliveryForTest(t, h, d, "defer", "")
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
			if resumed.Status != "awaiting_owner" || resumed.AttentionID == d.AttentionID || h.count("attentions") != 2 {
				t.Fatal(resumed)
			}
			expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, resumed), protocol.CodeForbidden)
			if h.task(source).Lifecycle != protocol.TaskCompleted {
				t.Fatal("original completed task changed")
			}
		})
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
	answerDeliveryForTest(t, h, d, "defer", "")
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
	if resumed.Status != "awaiting_owner" || resumed.AttentionID == d.AttentionID || h.count("attentions") != 2 {
		t.Fatal(resumed)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, resumed), protocol.CodeForbidden)
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
