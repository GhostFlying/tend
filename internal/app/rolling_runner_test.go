package app

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRollingSuccessorAdmitsUnrelatedWorkBeforeOriginalManagerReceiptsSettle(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		version     int64
		independent bool
	}{
		{"original-journal", protocol.MinimumRunnerProtocol, false},
		{"previous-generation", protocol.MinimumRunnerProtocol, true},
		{"current-generation", protocol.CurrentRunnerProtocol, true},
	} {
		t.Run(scenario.name+"-"+strconv.FormatInt(scenario.version, 10), func(t *testing.T) {
			ctx := t.Context()
			const host = "linux-1"
			h, old := managerClaimHarness(t)
			old.Sequence++
			old.Protocol, old.ProtocolMin, old.ProtocolMax = scenario.version, scenario.version, scenario.version
			if scenario.independent {
				// Begin this subtest with a previously installed journal.
				old.JournalID = old.InstanceID
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					rt, err := store.GetHostRuntime(ctx, tx.Q(), host)
					if err != nil {
						return err
					}
					rt.Runtime.JournalID = old.JournalID
					return tx.PutHostRuntime(ctx, host, rt)
				}); err != nil {
					t.Fatal(err)
				}
			}
			old.Build.PID, old.Build.BinarySHA256 = 91, strings.Repeat("a", 64)
			old.Capabilities = append(old.Capabilities, capabilityUpdater, protocol.CapabilityCompleteReleaseSet, protocol.CapabilityUpdaterSelfUpdate)
			if op := h.versionedHello(host, old); op.Error != nil {
				t.Fatal(op)
			}
			claimManager := func() protocol.Assignment {
				t.Helper()
				issue := createCanonicalOwnerTestIssue(t, h)
				request := protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Coordinate the Issue"}
				if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, request, rev(issue.Revision)), RequestIssueManager{}); op.Error != nil {
					t.Fatal(op)
				}
				return reviewClaimManager(t, h, old)
			}
			original := claimManager()
			native := func(operation string, payload any) protocol.Operation {
				t.Helper()
				return h.runAs(original, operation, payload, ManagerNativeCommand{OperationName: operation})
			}
			queue := func(method, payload string) string {
				t.Helper()
				id, err := h.app.QueueManagerNative(ctx, original.RunID, method, json.RawMessage(payload))
				if err != nil {
					t.Fatal(err)
				}
				op := native(protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{})
				var command protocol.ManagerNativeClaimResult
				if err := json.Unmarshal(op.Result, &command); op.Error != nil || err != nil || command.ID != id {
					t.Fatal("did not claim the original command", op, err)
				}
				return id
			}
			toolID := queue("tool_result", `{"thread_id":"original-thread","turn_id":"original-turn","call_id":"original-call","tool_result":{"Success":true,"Text":"original"}}`)
			toolReply := h.env(protocol.OpManagerNativeReply, runTarget(original.RunID), protocol.ManagerNativeReply{ID: toolID, Result: json.RawMessage(`null`), Error: "original tool result unconfirmed"}, nil)
			replyCommand := ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}
			// Deliver the tool reply but retain its original envelope as if the
			// transport acknowledgement was lost.
			if op := h.exec(runPrincipal(original.RunID, original.Role), toolReply, replyCommand); op.Error != nil {
				t.Fatal(op)
			}
			closeID := queue("close", "{}")
			// A real stop receipt releases the lease while the original close
			// remains uncertain. The native operation remains tied to its Run.
			if op := native(protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original process stopped"}); op.Error != nil {
				t.Fatal(op)
			}
			if run, lease := h.run(original.RunID), h.lease(original.RunID); run.ObservedState != protocol.RunFailed || lease.Status != store.LeaseReleased {
				t.Fatalf("original execution did not stop: state=%v lease=%+v", run.ObservedState, lease)
			}
			if h.countWhere("SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown'", closeID) != 1 {
				t.Fatal("stop settled the unknown close")
			}
			idle := claimManager()
			poll := h.env(protocol.OpManagerNativeClaim, runTarget(idle.RunID), protocol.ManagerNativeClaimPayload{}, nil)
			pollCommand := ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}
			checkEmptyPoll := func() {
				t.Helper()
				op := h.exec(runPrincipal(idle.RunID, idle.Role), poll, pollCommand)
				var result protocol.ManagerNativeClaimResult
				if err := json.Unmarshal(op.Result, &result); op.Error != nil || err != nil || result.ID != "" || h.run(idle.RunID).ObservedState != protocol.RunStarting {
					t.Fatal("original empty native poll lost its starting Run", op, err)
				}
			}
			checkEmptyPoll()
			c := selfUpgradeFixtureRelease(upgradeFixtureRelease(t, h))
			c.Manifest.Storage.RunnerJournal.Max = old.JournalVersion
			c.ServerUpdaterSupported = true
			c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals}
			if op := startPreparedTestUpgrade(h, "rolling-pending", c); op.Error != nil {
				t.Fatal(op)
			}
			batch := upgradeRead(h)
			server := ServerObservation{MainDrained: true, Healthy: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "current-server"}
			server.Updater = selfUpgradeObservation(batch, "server", server.ProcessInstanceID, c.ServerHash)
			for range 5 {
				if upgradeTick(h, server).Hosts[0].Phase == "warming" {
					break
				}
			}
			current := upgradeAck(h, host, protocol.RunnerNormal)
			old = &current
			heartbeat := func(r *protocol.RunnerRuntime) protocol.HeartbeatResult {
				t.Helper()
				r.Sequence++
				out, err := h.app.RuntimeHeartbeat(ctx, runner(host), protocol.HeartbeatPayload{Runtime: r})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			nativeSequence := int64(0)
			nativeUse := func() {
				t.Helper()
				nativeSequence++
				event := protocol.ManagerNativeEvent{Sequence: nativeSequence, Body: json.RawMessage(`{"thread_id":"original-idle","native":{"Kind":"request_usage"}}`)}
				command := ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}
				if op := h.runAs(idle, protocol.OpManagerNativeEvent, event, command); op.Error != nil {
					t.Fatal(op)
				}
			}
			nativeUse()
			batch = upgradeRead(h)
			b := protocol.UpgradeBinding{UpgradeID: batch.View.UpgradeID, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: batch.Hosts[0].TargetHash, InstanceID: host, ProcessInstanceID: "next-instance"}
			target := protocol.Target{Type: "host", ID: host}
			req := protocol.RollingRunnerGrantRequest{Binding: b, Updater: *selfUpgradeObservation(batch, host, old.InstanceID, old.Build.BinarySHA256)}
			old.PendingClaims, old.PendingOperations = 1, 83
			old.Blockers = []string{"pending_run_events", "pending_operation", "workspace_operation_active"}
			heartbeat(old)
			req.PendingProof = json.RawMessage(`{"sequence":1,"journal_id":"retained","operations":[]}`)
			env := h.env("runner.rolling_authorize", target, req, rev(old.AppliedControlRevision))
			op := h.exec(runner(host), env, AuthorizeRollingRunner{})
			var grant protocol.UpgradeGrant
			if err := json.Unmarshal(op.Result, &grant); op.Error != nil || err != nil || !grant.Allowed {
				t.Fatal("incumbent work blocked candidate launch", op, err)
			}
			reply := protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`)}
			originalReply := h.env(protocol.OpManagerNativeReply, runTarget(original.RunID), reply, nil)

			ctl := upgradeCtl(h, host)
			next := testRuntime(b.ProcessInstanceID)
			next.Generation, next.JournalID, next.JournalVersion = 1, b.ProcessInstanceID, c.Manifest.Storage.RunnerJournal.Max
			next.Build = protocol.RunnerBuild{PID: 92, BinarySHA256: b.ArtifactSHA256}
			next.Mode, next.AppliedControlRevision, next.UpgradeID = protocol.RunnerReady, ctl.Revision, b.UpgradeID
			if op := h.versionedHello(host, next); op.Error != nil {
				t.Fatal(op)
			}
			nativeUse()
			claimRequest := func(rt *protocol.RunnerRuntime) *protocol.Envelope {
				return h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: rt}, nil)
			}
			claim := func(rt *protocol.RunnerRuntime) (protocol.Assignment, bool) {
				return h.claimWith(host, claimRequest(rt))
			}
			if op, _ := h.createTask(owner, createTaskPayload{Title: "admit during warmup", Spec: testSpec}); op.Error != nil {
				t.Fatal(op)
			}
			// Persist an allocation while withholding its identity from the
			// runner fixture. This is the original claim after a lost reply.
			lostClaim := claimRequest(old)
			before, ok := h.claimWith(host, lostClaim)
			if !ok || before.RunID == original.RunID || before.RunID == idle.RunID {
				t.Fatal("candidate hello displaced the incumbent", before)
			}
			if op := h.runAs(before, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Error != nil {
				t.Fatal(op)
			}
			staleClaim := claimRequest(old)
			if _, ok := claim(next); ok {
				t.Fatal("uncommitted candidate claimed work")
			}
			if op, _ := h.createTask(owner, createTaskPayload{Title: "unrelated queued work", Spec: testSpec}); op.Error != nil {
				t.Fatal(op)
			}

			next.Capabilities = append(next.Capabilities, protocol.CapabilityUpdaterSelfUpdate)
			next.Updater = selfUpgradeObservation(batch, host, next.InstanceID, next.Build.BinarySHA256)
			next.Updater.SupervisedProcess.PID = int(next.Build.PID)
			heartbeat(next)
			upgradeTick(h, server)
			health, err := h.app.RunnerUpgradeHealth(ctx, runner(host), b)
			if err != nil || !health.Healthy || !health.AdmissionFenced || health.NewWorkAccepted {
				t.Fatal("healthy candidate was not separately fenced", health, err)
			}
			next.UpdaterReceipt = committedServerForTest(b)
			if scenario.version == protocol.CurrentRunnerProtocol {
				// A committed local candidate can restart while the coordinator
				// still holds the old admission pointer.
				next.InstanceID = "candidate-restarted"
				next.Generation++
				next.Sequence = 1
				next.Build.PID++
				next.Updater.SupervisedProcess.PID = int(next.Build.PID)
				next.Updater.SupervisedProcess.LaunchID = next.InstanceID
				next.UpdaterReceipt.CurrentProcessInstanceID = next.InstanceID
				if op := h.versionedHello(host, next); op.Error != nil {
					t.Fatal(op)
				}
			}
			heartbeat(next)
			upgradeTick(h, server)
			ctl = upgradeCtl(h, host)
			next.Mode, next.AppliedControlRevision = ctl.Mode, ctl.Revision
			heartbeat(next)
			drain := heartbeat(old).Control
			old.AppliedControlRevision = drain.Revision
			if _, ok := h.claimWith(host, staleClaim); ok {
				t.Fatal("draining predecessor claimed new work from an outstanding unallocated request")
			}
			if got, ok := h.claimWith(host, lostClaim); !ok || got.RunID != before.RunID || got.LeaseID != before.LeaseID {
				t.Fatal("lost reply did not retain original ownership", got)
			}
			if op := h.runAs(before, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "work stopped"}, Release{}); op.Error != nil {
				t.Fatal(op)
			}
			newRun, ok := claim(next)
			if !ok || newRun.TaskID == original.TaskID || newRun.TaskID == idle.TaskID || newRun.Protocol != protocol.CurrentRunnerProtocol {
				t.Fatal("committed successor could not claim unrelated work", newRun)
			}
			checkEmptyPoll()
			nativeUse()
			if h.countWhere("SELECT COUNT(*) FROM manager_native_events WHERE run_id=?", idle.RunID) != int(nativeSequence) {
				t.Fatal("candidate handoff interrupted receipt of incumbent native events")
			}
			var inventory store.AgentInventoryRow
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				var err error
				inventory, err = store.GetAgentInventory(ctx, tx.Q(), host)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			receipt := inventory.Receipt
			inventoryStatus, err := h.app.ReadAgentInventory(ctx, runner(host), protocol.AgentInventoryStatusPayload{Runtime: old,
				LocalRevision: receipt.LocalRevision, SnapshotID: receipt.SnapshotID, PublicSHA256: receipt.PublicSHA256})
			if err != nil || inventoryStatus.Matched == nil || inventoryStatus.Matched.SnapshotID != receipt.SnapshotID {
				t.Fatal("draining runner could not reconcile its committed inventory", inventoryStatus, err)
			}
			var projection protocol.AgentInventoryProjection
			if err := json.Unmarshal([]byte(inventory.CanonicalJSON), &projection); err != nil {
				t.Fatal(err)
			}
			unpublished := inventoryRequest(h, old, projection, receipt.PublicSHA256, receipt.LocalRevision+1, receipt.ServerRevision, "unpublished")
			if op := h.exec(runner(host), unpublished, RegisterAgentInventory{}); op.Status != protocol.OpFailed || protocol.CodeOf(op.Error) != protocol.CodeInvalidTransition {
				t.Fatal("original uncommitted inventory was not conclusively fenced", op)
			}
			if l := h.lease(idle.RunID); l.Status != store.LeaseActive || l.LeaseID != idle.LeaseID {
				t.Fatal("unrelated work displaced a live manager lease", l)
			}
			h.createTask(owner, createTaskPayload{Title: "await a physical slot", Spec: testSpec})
			if _, ok := claim(next); ok {
				t.Fatal("successor claimed the predecessor's occupied slot")
			}
			if h.countWhere("SELECT COUNT(*) FROM runs WHERE task_id=?", idle.TaskID) != 1 {
				t.Fatal("another Run took the active predecessor manager's work")
			}
			retirement := protocol.RunnerRetirement{UpgradeID: b.UpgradeID, Process: req.Updater.SupervisedProcess}
			retire := func() protocol.RunnerRetirementResult {
				t.Helper()
				op := h.exec(runner(host), h.env("runner.rolling_retire", target, retirement, nil), RetireRollingRunner{})
				var result protocol.RunnerRetirementResult
				if err := json.Unmarshal(op.Result, &result); op.Error != nil || err != nil {
					t.Fatal(op, err)
				}
				return result
			}
			heartbeat(old)
			if result := retire(); result.Allowed {
				t.Fatal("successor launch bypassed retirement readiness", result)
			}
			if op, err := h.app.Execute(ctx, runPrincipal(newRun.RunID, protocol.RoleIssueManager), originalReply, replyCommand); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("another Run can send the predecessor's original reply", op, err)
			}
			if op := h.exec(runPrincipal(original.RunID, original.Role), originalReply, replyCommand); op.Error != nil {
				t.Fatal("predecessor could not deliver its original reply after handoff", op)
			}
			if h.countWhere("SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='completed'", closeID) != 1 {
				t.Fatal("original receipt did not settle")
			}
			if op := h.exec(runPrincipal(original.RunID, original.Role), toolReply, replyCommand); op.Error != nil {
				t.Fatal("predecessor could not reconcile the unknown under its original identity", op)
			}
			if h.countWhere("SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown'", toolID) != 1 {
				t.Fatal("transport acceptance resolved the native outcome")
			}
			if result := retire(); result.Allowed {
				t.Fatal("retirement ignored the original active manager", result)
			}
			if op := h.runAs(idle, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original idle process exited", LastSequence: nativeSequence}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Error != nil {
				t.Fatal(op)
			}
			old.PendingClaims, old.PendingOperations, old.Blockers, old.Mode = 0, 0, nil, protocol.RunnerReady
			heartbeat(old)
			stopGrant := retire()
			if !stopGrant.Allowed || stopGrant.Confirmed {
				t.Fatal("settled predecessor did not get a separate stop grant", stopGrant)
			}
			// Report the supervisor's exact physical-exit proof to finish.
			retirement.AuthorizationID = stopGrant.AuthorizationID
			if result := retire(); !result.Confirmed {
				t.Fatal("exact retirement acknowledgement did not confirm", result)
			}
			if op := h.versionedHello(host, old); op.Error == nil {
				t.Fatal("retired process registered again")
			}
		})
	}
}

// The prepared candidate and supervisor receipts are observations from the
// fixture. Claims, rolling admission, journal routing and renewal use commands.
func TestRollingRunnerKeepsOriginalLeaseAfterIndependentJournalHello(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	cfg := testConfig()
	cfg.Hosts[0].Slots = 2
	if op := h.syncConfig(cfg); op.Error != nil {
		t.Fatal(op)
	}
	const host = "linux-1"
	old := upgradeAck(h, host, protocol.RunnerNormal)
	old.Sequence++
	old.Build.PID = 91
	old.Capabilities = append(old.Capabilities, protocol.CapabilityUsageCollection)
	old.Protocol, old.ProtocolMin, old.ProtocolMax = protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol
	if op := h.versionedHello(host, &old); op.Error != nil {
		t.Fatal(op)
	}
	h.createTask(owner, createTaskPayload{Title: "predecessor", Spec: testSpec})
	target := protocol.Target{Type: "host", ID: host}
	claim := h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &old}, nil)
	original, ok := h.claimWith(host, claim)
	if !ok {
		t.Fatal("old runner did not claim")
	}
	c.ServerUpdaterSupported = true
	if op := startPreparedTestUpgrade(h, "rolling", c); op.Error != nil {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	server := ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "current-server", Healthy: true}
	server.Updater = selfUpgradeObservation(batch, "server", server.ProcessInstanceID, c.ServerHash)
	for range 5 {
		view := upgradeTick(h, server)
		if view.Hosts[0].Phase == "warming" {
			break
		}
	}
	old = upgradeAck(h, host, protocol.RunnerNormal)
	old.Blockers = []string{"run:" + original.RunID}
	batch = upgradeRead(h)
	b := protocol.UpgradeBinding{UpgradeID: batch.View.UpgradeID, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: batch.Hosts[0].TargetHash, InstanceID: host, ProcessInstanceID: "next-instance"}
	req := protocol.RollingRunnerGrantRequest{Binding: b, Updater: *selfUpgradeObservation(batch, host, old.InstanceID, old.Build.BinarySHA256)}
	var grant protocol.UpgradeGrant
	op := h.exec(runner(host), h.env("runner.rolling_authorize", target, req, rev(old.AppliedControlRevision)), AuthorizeRollingRunner{})
	if op.Error != nil || json.Unmarshal(op.Result, &grant) != nil || !grant.Allowed {
		t.Fatalf("rolling grant refused at %s/%s: %+v", batch.View.Phase, batch.View.Hosts[0].Phase, op)
	}
	ctl := upgradeCtl(h, host)
	if stop := h.exec(runner(host), h.env("runner.upgrade_authorize", target, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{}); stop.Error == nil {
		t.Fatal("rolling grant allowed stopping the predecessor")
	}
	candidate := testRuntime(b.ProcessInstanceID)
	candidate.Generation = 1
	candidate.JournalID = b.ProcessInstanceID
	candidate.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
	candidate.Build = protocol.RunnerBuild{BinarySHA256: b.ArtifactSHA256, PID: 92}
	candidate.Mode = protocol.RunnerReady
	candidate.UpgradeID = b.UpgradeID
	candidate.AppliedControlRevision = ctl.Revision
	op = h.versionedHello(host, candidate)
	var hello protocol.HelloResult
	if op.Error != nil || json.Unmarshal(op.Result, &hello) != nil || len(hello.Reassigned) != 0 {
		t.Fatalf("new journal consumed predecessor claim: %+v", op)
	}
	old.Sequence++
	ref := protocol.LeaseRef{RunID: original.RunID, LeaseID: original.LeaseID}
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &old, Runs: []protocol.LeaseRef{ref}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed || hb.Control == nil || hb.Control.Mode != protocol.RunnerNormal {
		t.Fatalf("old lease did not survive handoff: %+v %v", hb, err)
	}
	// Owner commands for an existing source follow its Run across handoff.
	reg := protocol.NativeUsageRegistration{RunID: original.RunID, ThreadID: "old-thread", SessionID: "old-thread", Provider: "codex"}
	if op := h.exec(runner(host), h.env(protocol.OpRunnerUsageRegister, target, reg, nil), RegisterNativeUsage{}); op.Error != nil {
		t.Fatal(op)
	}
	collection := protocol.NewUsageCollection(false, true)
	observation := protocol.UsageCollectionObservation{LocalSourceID: "local-old", RunID: original.RunID, ThreadID: reg.ThreadID, SessionID: reg.SessionID, Collection: collection, Sequence: 1}
	if op := h.exec(runner(host), h.env(protocol.OpRunnerUsageCollection, target, observation, nil), ObserveUsageCollection{}); op.Error != nil {
		t.Fatal(op)
	}
	command := h.env(protocol.OpResumeUsageCollection, protocol.Target{Type: "usage_source", ID: original.RunID + "/old-thread"}, protocol.UsageCollectionControl{}, rev(1))
	if op := h.exec(owner, command, ControlUsageCollection{Resume: true}); op.Error != nil {
		t.Fatal(op)
	}
	candidate.Capabilities = append(candidate.Capabilities, protocol.CapabilityUsageCollection)
	candidate.Sequence++
	nextHeartbeat, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate})
	if err != nil || nextHeartbeat.Control == nil || len(nextHeartbeat.Control.UsageCollectionCommands) != 0 {
		t.Fatal("foreign source sent to new journal", nextHeartbeat, err)
	}
	old.Sequence++
	hb, err = h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &old, Runs: []protocol.LeaseRef{ref}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed || hb.Control == nil || len(hb.Control.UsageCollectionCommands) != 1 || hb.Control.UsageCollectionCommands[0].RequestID != command.RequestID {
		t.Fatal("draining journal did not receive the original command and renewal", hb, err)
	}
	candidate.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate, Runs: []protocol.LeaseRef{ref}}); err == nil {
		t.Fatal("another journal renewed original lease")
	}
	// Post-commit admission of the candidate is covered by supervisor tests.
	// Supply its committed receipt and observe the business transition here.
	candidate.Capabilities = append(candidate.Capabilities, protocol.CapabilityUpdaterSelfUpdate)
	candidate.Updater = selfUpgradeObservation(batch, host, candidate.InstanceID, candidate.Build.BinarySHA256)
	candidate.Updater.SupervisedProcess.PID = int(candidate.Build.PID)
	candidate.UpdaterReceipt = &protocol.UpdaterReceipt{Phase: "committed", CommittedConfirmed: true, Binding: b, CurrentSHA256: b.ArtifactSHA256, CurrentProcessInstanceID: candidate.InstanceID}
	candidate.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate}); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, server)
	ctl = upgradeCtl(h, host)
	if ctl.Mode != protocol.RunnerNormal {
		t.Fatal("old lease kept the committed candidate in maintenance", upgradeRead(h).View)
	}
	candidate.AppliedControlRevision = ctl.Revision
	candidate.Mode = protocol.RunnerNormal
	candidate.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate}); err != nil {
		t.Fatal(err)
	}
	_, sameRepo := h.createTask(owner, createTaskPayload{Title: "same repository", Spec: testSpec})
	newClaim := func(rt *protocol.RunnerRuntime) (protocol.Assignment, bool) {
		return h.claimWith(host, h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: rt}, nil))
	}
	if _, ok := newClaim(&old); ok {
		t.Fatal("draining runner admitted new work")
	}
	if _, ok := newClaim(candidate); ok {
		t.Fatal("successor started preparing a repository still owned by the legacy runner")
	}
	other := h.createReportTask("independent workspace", temporaryReportSpec())
	unrelated, ok := newClaim(candidate)
	if !ok || unrelated.TaskID != other.TaskID {
		t.Fatal("original repository preparation blocked independent work", unrelated)
	}
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(other.TaskID), nil, rev(h.task(other.TaskID).ControlRevision)), CancelTask{}); op.Error != nil {
		t.Fatal(op)
	}
	if op := h.runAs(unrelated, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "independent work stopped"}, Release{}); op.Error != nil {
		t.Fatal(op)
	}
	if op := h.runAs(original, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Error != nil {
		t.Fatal(op)
	}
	assigned, ok := newClaim(candidate)
	if !ok || assigned.TaskID != sameRepo.TaskID {
		t.Fatal("original preparation receipt did not unblock the repository", assigned)
	}
	h.createTask(owner, createTaskPayload{Title: "capacity", Spec: testSpec})
	if _, ok := newClaim(candidate); ok {
		t.Fatal("physical host capacity counted twice")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), original.RunID)
		if err == nil && (lease.Status != store.LeaseActive || lease.LeaseID != original.LeaseID) {
			t.Fatal("predecessor lease was replaced", lease)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Seed a confirmed physical retirement to cover dispatch of a late command.
	// Process stop and acknowledgement are exercised by the three-process fixture.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO runner_retirements(host_id,instance_id,upgrade_id,journal_id,authorization_id,process_json,confirmed_at) VALUES(?,?,?,?,?,?,1)`, host, old.InstanceID, batch.View.UpgradeID, old.JournalID, "retired-grant", "{}"); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM draining_runners WHERE host_id=? AND instance_id=?`, host, old.InstanceID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	candidate.Sequence++
	hb, err = h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate})
	retired := hb.RetiredWork[old.JournalID]
	if err != nil || hb.Control == nil || len(hb.Control.UsageCollectionCommands) != 0 || len(retired.Usage) != 1 || retired.Usage[0].RequestID != command.RequestID {
		t.Fatal("late command did not retain its journal and request", hb, err)
	}

}

func TestBusyGenerationReceivesAutomaticRollingTarget(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	if op := h.syncConfig(testConfig()); op.Error != nil {
		t.Fatal(op)
	}
	const host = "linux-1"
	// Begin with an already installed independent journal; this test drives
	// the subsequent release through the ordinary fleet upgrade entry point.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		rt, err := store.GetHostRuntime(t.Context(), tx.Q(), host)
		if err != nil {
			return err
		}
		rt.Runtime.JournalID = rt.Runtime.InstanceID
		return tx.PutHostRuntime(t.Context(), host, rt)
	}); err != nil {
		t.Fatal(err)
	}
	h.createTask(owner, createTaskPayload{Title: "active predecessor", Spec: testSpec})
	rt := upgradeAck(h, host, protocol.RunnerNormal)
	target := protocol.Target{Type: "host", ID: host}
	claim := h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &rt}, nil)
	as, ok := h.claimWith(host, claim)
	if !ok {
		t.Fatal("no original assignment")
	}
	c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals}
	c.ServerUpdaterSupported = true
	if op := startPreparedTestUpgrade(h, "next", c); op.Error != nil {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	server := ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "current-server", Healthy: true}
	server.Updater = selfUpgradeObservation(batch, "server", server.ProcessInstanceID, c.ServerHash)
	for range 5 {
		if upgradeTick(h, server).Hosts[0].Phase == "warming" {
			break
		}
	}
	rt = upgradeAck(h, host, protocol.RunnerNormal)
	upgradeTick(h, server)
	rt.Sequence++
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed || hb.Control == nil || hb.Control.Upgrade == nil || !hb.Control.Upgrade.Rolling || hb.Control.Upgrade.PrepareOnly || !hb.Control.Upgrade.Updater.Authorized {
		t.Fatalf("busy generation missing rolling target: control=%+v phase=%+v journal=%q err=%v", hb.Control, upgradeRead(h).View.Hosts, rt.JournalID, err)
	}
}

func TestRollingUnconfirmedLaunchKeepsIncumbentClaiming(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	const host = "linux-1"
	if op := h.syncConfig(testConfig()); op.Error != nil {
		t.Fatal(op)
	}
	incumbent := upgradeAck(h, host, protocol.RunnerNormal)
	incumbent.Sequence++
	incumbent.Build.PID = 91
	if op := h.versionedHello(host, &incumbent); op.Error != nil {
		t.Fatal(op)
	}
	c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals}
	c.ServerUpdaterSupported = true
	if op := startPreparedTestUpgrade(h, "failed-warmup", c); op.Error != nil {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	server := ServerObservation{MainDrained: true, Healthy: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "current-server"}
	server.Updater = selfUpgradeObservation(batch, "server", server.ProcessInstanceID, c.ServerHash)
	for range 5 {
		if upgradeTick(h, server).Hosts[0].Phase == "warming" {
			break
		}
	}
	old := upgradeAck(h, host, protocol.RunnerNormal)
	batch = upgradeRead(h)
	target := protocol.Target{Type: "host", ID: host}
	b := protocol.UpgradeBinding{UpgradeID: batch.View.UpgradeID, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256,
		ArtifactSHA256: batch.Hosts[0].TargetHash, InstanceID: host, ProcessInstanceID: "no-start-confirmation"}
	req := protocol.RollingRunnerGrantRequest{Binding: b, Updater: *selfUpgradeObservation(batch, host, old.InstanceID, old.Build.BinarySHA256)}
	authorize := func() protocol.UpgradeGrant {
		t.Helper()
		op := h.exec(runner(host), h.env("runner.rolling_authorize", target, req, rev(old.AppliedControlRevision)), AuthorizeRollingRunner{})
		var g protocol.UpgradeGrant
		if err := json.Unmarshal(op.Result, &g); op.Error != nil || err != nil || !g.Allowed {
			t.Fatal(op, err)
		}
		return g
	}
	first, retry := authorize(), authorize()
	if retry.Binding != first.Binding || !retry.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatal("original authorization changed after a lost reply", first, retry)
	}
	h.clock.Advance(time.Duration(upgradeRead(h).Deadline-h.clock.Now().UnixMilli()+1) * time.Millisecond)
	if view := upgradeTick(h, server); view.Status != "attention" || upgradeCtl(h, host).Mode != protocol.RunnerNormal {
		t.Fatal("uncertain candidate displaced the incumbent", view)
	}
	old = upgradeAck(h, host, protocol.RunnerNormal)
	candidate := testRuntime(b.ProcessInstanceID)
	candidate.JournalID, candidate.UpgradeID, candidate.Mode = b.ProcessInstanceID, b.UpgradeID, protocol.RunnerReady
	candidate.Build = protocol.RunnerBuild{PID: 92, BinarySHA256: b.ArtifactSHA256}
	if op := h.versionedHello(host, candidate); op.Error == nil {
		t.Fatal("candidate registered without verified in-window launch")
	}
	want := h.createReportTask("work accepted after candidate timeout", temporaryReportSpec())
	got, ok := h.claimWith(host, h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Runtime: &old, Worker: h.worker(host), WorkspaceProtocol: 1}, nil))
	if !ok || got.TaskID != want.TaskID {
		t.Fatal("unconfirmed candidate disrupted old claims", got)
	}
	// Reconcile a definitive supervisor cancellation even while the original
	// process owns active work and would never report maintenance-ready.
	old.Sequence++
	old.Updater = &req.Updater
	old.UpdaterReceipt = &protocol.UpdaterReceipt{Phase: "cancelled", Binding: b,
		CurrentProcessInstanceID: old.InstanceID, CurrentSHA256: old.Build.BinarySHA256}
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &old}); err != nil {
		t.Fatal(err)
	}
	if view := upgradeTick(h, server); view.Hosts[0].Phase != "cancelled" || upgradeCtl(h, host).Mode != protocol.RunnerNormal {
		t.Fatal("confirmed cancellation blocked on incumbent workload", view)
	}
}
