package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// Real owner policy, whole-PR review, Task binding and lease establish authority.
// Release metadata and subsequent physical installation receipts are test doubles.
func autoDeploymentLifecycleFixture(t *testing.T, manual bool, sourceBacked ...bool) (*harness, protocol.Issue, protocol.Assignment, StartUpgrade) {
	t.Helper()
	h, runtime, issue, sourceTask, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, sourceTask, acceptance)
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, second)
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		rt := runtime
		if host == "linux-2" {
			rt = testRuntime("second-updater")
			rt.JournalVersion = runtime.JournalVersion
		}
		if rt.Generation == 0 {
			rt.Generation = 1
		}
		rt.Sequence++
		rt.Capabilities = append(rt.Capabilities, capabilityUpdater, protocol.CapabilityCompleteReleaseSet, protocol.CapabilityUpdaterSelfUpdate)
		rt.Build.BinarySHA256, rt.Build.Version = strings.Repeat("a", 64), "previous-release"
		// Configure the fleet before claiming the continuing live execution.
		payload := protocol.HelloPayload{WorkspaceProtocol: 1, Worker: h.worker(host), Runtime: rt}
		if op := h.exec(runner(host), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: host}, payload, nil), Hello{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	if manual {
		issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
	} else {
		issue.Plan.PRPolicy.Deployment = &protocol.IssueDeploymentScope{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
		if len(sourceBacked) > 0 && sourceBacked[0] {
			if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
			h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared), WithMainOwner(owner.Subject))
			issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
			issue.Plan.PRPolicy.Deployment = &protocol.IssueDeploymentScope{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
			conversation := "issue:" + issue.IssueID
			submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Authorize automatic integration and installation on this exact service and its two runners"}, nil), SubmitMessage{})
			if submitted.Status != protocol.OpAccepted {
				t.Fatal(submitted)
			}
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			turn := startHostedReview(t, h, conversation, delivery)
			source := turn.MessageID
			principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + issue.IssueID, Thread: turn.ThreadID, ConversationID: conversation}
			payload := protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: rev(1)}}
			command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "update"}, ExecutionID: turn.RequestID, EffectSeq: 1}
			op := h.exec(principal, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), command)
			issue = canonicalLifecycleIssue(t, op)
			if issue.Plan.PRPolicy.Authority.SourceMessageID != source {
				t.Fatal("owner relay did not retain its actual authorization source")
			}
		} else {
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
		}
	}
	// Keep the original real report/review fixture, then create one continuing
	// execution only after its owner and fleet configuration are established.
	rawSpec, err := json.Marshal(h.storedSpec(sourceTask, 1))
	if err != nil {
		t.Fatal(err)
	}
	var spec protocol.OwnerTaskSpec
	if err := json.Unmarshal(rawSpec, &spec); err != nil {
		t.Fatal(err)
	}
	created := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{Title: "Continue this reviewed delivery", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var task taskResult
	if err := json.Unmarshal(created.Result, &task); err != nil {
		t.Fatal(err)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task.TaskID {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: candidate.DeliveryID, TaskID: as.TaskID}))
	if !linked.Valid {
		t.Fatal("original whole-PR verdict lost its real scope", linked)
	}
	candidate.ProviderState, candidate.MergeSHA = "merged", strings.Repeat("c", 40)
	candidate.TargetSHA, candidate.Deploy = candidate.MergeSHA, true
	candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
	candidate.DeploymentScope = "Install the reviewed source only on the explicitly authorized server and runners"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, candidate.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.State, row.Observation.MergeSHA = "merged", candidate.MergeSHA
		row.Observation.TargetSHA, row.Observation.TargetContainsMerge = candidate.TargetSHA, true
		return putDeliveryObservationForTest(tx, t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	duty := prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
	if manual {
		answerDeliveryForTest(t, h, duty, "approve", "")
	} else if duty.Status != "authorized" || duty.Authorization == nil || duty.Authorization.Mode != "auto" || duty.AttentionID != "" {
		t.Fatal("automatic scope was not genuinely authorized", duty)
	}
	bindSelfCandidateForTest(t, h, issue.IssueID, as.TaskID)
	var schema int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
	}); err != nil {
		t.Fatal(err)
	}
	serverHash, runnerHash := strings.Repeat("e", 64), strings.Repeat("f", 64)
	manifest := release.Manifest{Format: 1, ReleaseID: "authorized-release", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: schema, Max: schema}, RunnerJournal: release.Range{Min: 9, Max: runtime.JournalVersion}}, Artifacts: []release.Artifact{{Role: "server", OS: "linux", Arch: "amd64", Path: "server", Size: 20, SHA256: serverHash}, {Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: 10, SHA256: runnerHash}}}
	manifestHash := strings.Repeat("d", 64)
	set := &release.Set{Format: 2, ReleaseID: manifest.ReleaseID, Commit: candidate.TargetSHA, OS: "linux", Arch: "amd64", ManifestSHA256: manifestHash, Artifacts: append([]release.Artifact{}, manifest.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		set.Artifacts = append(set.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: 12, SHA256: strings.Repeat("1", 64)})
	}
	raw, _ := json.Marshal(set)
	c := StartUpgrade{ServerUpdaterSupported: true, Manifest: manifest, ManifestSHA256: manifestHash, ManifestSize: 128, ReleaseSet: set, ReleaseSetSHA256: strings.Repeat("2", 64), ReleaseSetSize: int64(len(raw)), ServerHash: serverHash, ServerInstance: "server", DeliveryTaskID: as.TaskID, ArtifactSources: map[string]release.SourceIdentity{serverHash: {Commit: candidate.TargetSHA}, runnerHash: {Commit: candidate.TargetSHA}}}
	return h, canonicalTaskIssue(t, h, issue.IssueID), as, c
}

func TestAutoDeploymentLifecycleStartsAndContinuesOriginalBoundTask(t *testing.T) {
	h, _, as, c := autoDeploymentLifecycleFixture(t, false)
	tasks, runs := h.count("tasks"), h.count("runs")
	if run := h.run(as.RunID); run.ObservedState != protocol.RunRunning || h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("fixture did not retain the actual original running lease", run.ObservedState)
	}
	env := h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "original-authorized-install"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0))
	if recorded, err := h.app.CheckUpgradeStart(t.Context(), env, c); err != nil || recorded != nil {
		t.Fatal("valid original automatic deployment failed preflight", recorded, err)
	}
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, c)
	if op.Status != protocol.OpSucceeded {
		t.Fatal("valid original automatic deployment was refused", op)
	}
	batch := upgradeRead(h)
	if batch.DeliveryTaskID != as.TaskID || batch.StartRequestID != env.RequestID || batch.View.UpgradeID != env.Target.ID || batch.View.SourceCommit != c.ReleaseSet.Commit {
		t.Fatal("admission changed the original source or execution identity", batch)
	}
	// The same request can reconcile a lost reply; a new ID cannot install twice.
	if again := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, c); again.Status != protocol.OpSucceeded || h.count("upgrades") != 1 {
		t.Fatal("original start retry duplicated the installation", again)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		batch.View.Status, batch.View.Phase = "completed", "completed"
		batch.View.Revision++
		return tx.PutUpgrade(t.Context(), batch)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := deliveryTaskCurrent(t.Context(), tx.Q(), as.TaskID)
		if err == nil && !current {
			t.Fatal("completed original auto installation cannot continue its same bound Task")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	duplicate := h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "unwanted-second-install"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0))
	if _, err := h.app.CheckUpgradeStart(t.Context(), duplicate, c); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("completed binding authorized another installation", err)
	}
	if h.count("tasks") != tasks || h.count("runs") != runs || h.count("upgrades") != 1 || h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("installation admission recreated execution or released original lease")
	}
}

func TestAutoDeploymentCompletedBoundRequiresExistingCurrentGrant(t *testing.T) {
	h, _, as, c := autoDeploymentLifecycleFixture(t, false)
	// Isolate the second consumer using externally persisted installation facts;
	// the separate admission test creates its binding via Start, not installation.
	batch := store.UpgradeRow{DeliveryTaskID: as.TaskID, StartRequestID: "original-start", View: protocol.UpgradeView{UpgradeID: "original-bound-install", Status: "completed", Phase: "completed", SourceCommit: c.ReleaseSet.Commit, Revision: 2, CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutUpgrade(t.Context(), batch); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=?`, batch.View.UpgradeID, as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := h.task(as.TaskID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := deliveryTaskCurrent(t.Context(), tx.Q(), as.TaskID)
		if err == nil && !current {
			t.Fatal("existing automatic grant was treated as a missing manual answer")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, h.task(as.TaskID)) {
		t.Fatal("read-only current check changed original Task")
	}
}

func TestAutoDeploymentLifecycleRejectsRevokedAuthorityAtBothConsumers(t *testing.T) {
	for _, change := range []string{"manual-withdrawn", "manual-edited", "auto-policy", "auto-source", "auto-scope", "auto-repository"} {
		t.Run(change, func(t *testing.T) {
			h, issue, as, c := autoDeploymentLifecycleFixture(t, strings.HasPrefix(change, "manual"), change == "auto-source")
			duty := deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)
			switch change {
			case "manual-withdrawn", "auto-source":
				// Consumed sources cannot be withdrawn via the ordinary input UI.
				// Inject this retained control fact, as the historical source tests
				// do, to verify that neither consumer ignores an actual revocation.
				source := issue.Plan.PRPolicy.Authority
				message := ""
				if source != nil {
					message = source.SourceMessageID
				}
				if change == "manual-withdrawn" {
					message = deliveryDecisionForTest(t, h, duty).AnsweredMessageID
				}
				if message == "" {
					t.Fatal("source was not genuinely established")
				}
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO conversation_message_controls(message_id,withdrawn_at) VALUES(?,1) ON CONFLICT(message_id) DO UPDATE SET withdrawn_at=1`, message)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "manual-edited":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET content_revision=content_revision+1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "auto-policy":
				setPRPolicyForTest(t, h, issue.IssueID, "manual")
			case "auto-scope", "auto-repository":
				if change == "auto-scope" {
					issue.Plan.PRPolicy.Deployment.HostIDs = []string{"linux-1"}
				} else {
					issue.Plan.PRPolicy.Repository = "example/different"
				}
				canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
			}
			env := h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "refused-install"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0))
			if _, err := h.app.CheckUpgradeStart(t.Context(), env, c); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("revoked authority admitted installation", err)
			}
			if h.count("upgrades") != 0 {
				t.Fatal("failed preflight created installation")
			}
			// Only external installation facts are supplied here, not authority.
			// The completed-bound consumer must still reject the same revocation.
			row := store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "retained-original-install", Status: "completed", Phase: "completed", Revision: 2, CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.PutUpgrade(t.Context(), row); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=?`, row.View.UpgradeID, as.TaskID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				current, err := deliveryTaskCurrent(t.Context(), tx.Q(), as.TaskID)
				if err != nil || current {
					t.Fatal("revoked authority became continuation or a store failure", current, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAutoDeploymentLifecycleManualAndBoundBatchStates(t *testing.T) {
	h, _, as, c := autoDeploymentLifecycleFixture(t, true)
	env := h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "manual-original-install"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0))
	if _, err := h.app.CheckUpgradeStart(t.Context(), env, c); err != nil {
		t.Fatal("exact current manual approval lost installation authority", err)
	}
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	row := upgradeRead(h)
	for _, state := range []string{"running", "attention", "cancelled", "completed"} {
		row.View.Status = state
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), row) }); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			current, err := deliveryTaskCurrent(t.Context(), tx.Q(), as.TaskID)
			if err != nil || current != (state == "completed") {
				t.Fatal("batch state became completion or exact approval was lost", state, current, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
