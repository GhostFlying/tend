package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func ordinaryRetainedRunFixture(t *testing.T, create ...func(*harness)) (*harness, protocol.Assignment, *protocol.RunnerRuntime) {
	t.Helper()
	h := newHarness(t)
	ctx := t.Context()
	if len(create) == 0 {
		h.createTask(owner, createTaskPayload{Title: "Resume the original worker", Spec: testSpec})
	} else {
		create[0](h)
	}
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("ordinary Run not claimed")
	}
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "linux-1", LocalID: "implementer"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "orca", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model", Effort: "high"}, Worker: protocol.WorkerOrca, BackendID: "orca"}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.InsertRunExecution(ctx, as.RunID, pin); err != nil {
			return err
		}
		if err := tx.UpdateRunObserved(ctx, as.RunID, protocol.RunRunning, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		rt.Runtime.Capabilities = append(rt.Runtime.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityRetainedReconciliation, protocol.CapabilityStrictRetainedResume)
		rt.Runtime.JournalVersion = 16
		rt.Runtime.AgentExecutors = []protocol.AgentExecutor{{Pin: pin, Available: true}}
		return tx.PutHostRuntime(ctx, "linux-1", rt)
	}); err != nil {
		t.Fatal(err)
	}
	prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "ordinary-recovery", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "old", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "ordinary-recovery", InstanceID: "server", ProcessInstanceID: "new", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: as.TaskID, Run: protocol.RunView{RunID: as.RunID, Role: as.Role, Generation: as.Generation, SpecVersion: as.SpecVersion, Host: "linux-1", Observed: protocol.RunRunning}}}}}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "new", SHA256: p.Successor.ArtifactSHA256}
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
		t.Fatal(err)
	}
	rt := retainedHelloRuntime(t, h, true)
	return h, as, rt
}

func continuationPayload(t *testing.T, h *harness, run string) protocol.ContinueRetainedRunPayload {
	t.Helper()
	s, err := h.app.RetainedRunReconciliation(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CanContinue {
		t.Fatal("fixture is not ready", s.Blockers)
	}
	return protocol.ContinueRetainedRunPayload{RetainedRunIdentity: s.Identity, Protections: s.Protections, ProtectionSetSHA256: s.ProtectionSetSHA256, Reason: "Independently reconcile the retained worker and attach only its original native identity"}
}

func TestRetainedRunContinuationRestoresOnlyOriginalHeldLease(t *testing.T) {
	h, as, rt := ordinaryRetainedRunFixture(t)
	ctx := t.Context()
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	op, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if err != nil {
		t.Fatal(err)
	}
	var hello protocol.HelloResult
	if json.Unmarshal(op.Result, &hello) != nil || len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictRetain {
		t.Fatal("finished maintenance must still retain the original Run", op)
	}
	p := continuationPayload(t, h, as.RunID)
	h.clock.Advance(2 * time.Minute)
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		return tx.SetLease(ctx, as.LeaseID, beforeLease.Status, store.LeaseLapsed, beforeLease.ExpiresAt, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	request := h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(h.task(as.TaskID).ControlRevision))
	authorized := h.exec(owner, request, ContinueRetainedRun{})
	if authorized.Status != protocol.OpSucceeded {
		t.Fatal(authorized)
	}
	if again := h.exec(owner, request, ContinueRetainedRun{}); again.Error == nil || again.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("old continuation command did not observe current protection revision", again)
	}
	if h.lease(as.RunID).Status != store.LeaseLapsed || !reflect.DeepEqual(beforeRun, h.run(as.RunID)) {
		t.Fatal("authorization itself renewed or changed the original Run")
	}
	rt.Sequence++
	op, err = retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(op.Result, &hello) != nil || len(hello.Runs) != 1 || hello.Runs[0].Verdict != "continue_retained" || len(hello.Reassigned) != 0 {
		t.Fatal("independent authorization did not reattach the original journal", op)
	}
	if l := h.lease(as.RunID); l.LeaseID != beforeLease.LeaseID || l.Status != store.LeaseActive || l.ExpiresAt <= h.clock.Now().UnixMilli() {
		t.Fatal("original expired lease was not restored by Hello", l)
	}
	if r := h.run(as.RunID); r.RunID != beforeRun.RunID || r.ActionID != beforeRun.ActionID || r.Generation != beforeRun.Generation {
		t.Fatal("continuation replaced original business identity", r)
	}
}

func TestRetainedRunContinuationRejectsUnsettledOrChangedBusiness(t *testing.T) {
	for _, kind := range []string{"paused", "cancelled", "manager", "released-lease", "stale-control", "wrong-lease", "changed-pin", "changed-protection", "pending-native", "unapplied-native", "archive-prepared", "old-runner", "old-journal", "runner-maintenance"} {
		t.Run(kind, func(t *testing.T) {
			h, as, _ := ordinaryRetainedRunFixture(t)
			ctx := t.Context()
			p := continuationPayload(t, h, as.RunID)
			expected := h.task(as.TaskID).ControlRevision
			if err := h.store.Write(ctx, func(tx *store.Tx) error {
				switch kind {
				case "manager":
					_, err := tx.Exec(ctx, `UPDATE runs SET role=? WHERE run_id=?`, protocol.RoleIssueManager, as.RunID)
					return err
				case "paused":
					_, err := tx.Exec(ctx, `UPDATE tasks SET control_mode='paused' WHERE task_id=?`, as.TaskID)
					return err
				case "cancelled":
					_, err := tx.Exec(ctx, `UPDATE tasks SET lifecycle='cancelled' WHERE task_id=?`, as.TaskID)
					return err
				case "released-lease":
					_, err := tx.Exec(ctx, `UPDATE leases SET status='released' WHERE lease_id=?`, as.LeaseID)
					return err
				case "pending-native":
					_, err := tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'turn','{}','unknown',?)`, h.ids.New(protocol.PrefixRequest), as.RunID, h.clock.Now().UnixMilli())
					return err
				case "unapplied-native":
					_, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json,applied) VALUES(?,1,'{}',0)`, as.RunID)
					return err
				case "archive-prepared":
					_, err := tx.Exec(ctx, `UPDATE archive_preparation SET prepared=1 WHERE singleton=1`)
					return err
				case "old-runner", "old-journal", "runner-maintenance":
					row, err := store.GetHostRuntime(ctx, tx.Q(), "linux-1")
					if err != nil {
						return err
					}
					switch kind {
					case "old-journal":
						row.Runtime.JournalVersion = 15
					case "runner-maintenance":
						row.Runtime.Mode = protocol.RunnerMaintenance
					default:
						row.Runtime.Capabilities = slices.DeleteFunc(row.Runtime.Capabilities, func(c string) bool { return c == protocol.CapabilityStrictRetainedResume })
					}
					return tx.PutHostRuntime(ctx, "linux-1", row)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "stale-control":
				expected--
			case "wrong-lease":
				p.LeaseID = "another-lease"
			case "changed-pin":
				p.Execution.AgentRevision++
			case "changed-protection":
				p.Protections[0].MaintenanceID = "another-capture"
			}
			beforeRun, beforeLease, beforeTask := h.run(as.RunID), h.lease(as.RunID), h.task(as.TaskID)
			op, err := h.app.Execute(ctx, owner, h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(expected)), ContinueRetainedRun{})
			if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("unsafe independent continuation accepted", kind, op)
			}
			if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) || !reflect.DeepEqual(beforeTask, h.task(as.TaskID)) {
				t.Fatal("rejected continuation changed original business identity")
			}
		})
	}
}

func TestRetainedRunContinuationWithoutOriginalJournalDoesNotReassign(t *testing.T) {
	h, as, rt := ordinaryRetainedRunFixture(t)
	p := continuationPayload(t, h, as.RunID)
	op := h.exec(owner, h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(h.task(as.TaskID).ControlRevision)), ContinueRetainedRun{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.lease(as.RunID)
	response, err := retainedHello(t, h, rt)
	if err != nil {
		t.Fatal(err)
	}
	var hello protocol.HelloResult
	if json.Unmarshal(response.Result, &hello) != nil || len(hello.Reassigned) != 0 || len(hello.Runs) != 0 || !reflect.DeepEqual(before, h.lease(as.RunID)) {
		t.Fatal("missing original journal was recreated or renewed", response)
	}
}

func TestRetainedRunContinuationHeartbeatCannotConsumePermission(t *testing.T) {
	h, as, rt := ordinaryRetainedRunFixture(t)
	if _, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil {
		t.Fatal(err)
	}
	p := continuationPayload(t, h, as.RunID)
	op := h.exec(owner, h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(h.task(as.TaskID).ControlRevision)), ContinueRetainedRun{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.lease(as.RunID)
	rt.Sequence++
	response, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: rt, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
	if err != nil || len(response.Runs) != 1 || response.Runs[0].Renewed || !reflect.DeepEqual(before, h.lease(as.RunID)) {
		t.Fatal("heartbeat bypassed original Hello", response, err)
	}
}

func TestRetainedRunContinuationNewCaptureAndNewPendingInvalidatePermission(t *testing.T) {
	for _, kind := range []string{"new-capture", "new-pending", "new-control"} {
		t.Run(kind, func(t *testing.T) {
			h, as, rt := ordinaryRetainedRunFixture(t)
			ctx := t.Context()
			p := continuationPayload(t, h, as.RunID)
			if op := h.exec(owner, h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(h.task(as.TaskID).ControlRevision)), ContinueRetainedRun{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// Activate exactly once; later unrelated facts must invalidate renewal too.
			if _, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil {
				t.Fatal(err)
			}
			if kind == "new-capture" {
				archive, err := h.app.ArchivePreparation(ctx)
				if err != nil {
					t.Fatal(err)
				}
				prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: archive.Revision}, true)
				if err != nil {
					t.Fatal(err)
				}
				var previous protocol.CommittedMaintenanceLedger
				if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
					var e error
					previous, e = store.GetCommittedMaintenance(ctx, tx.Q(), "ordinary-recovery")
					return e
				}); err != nil {
					t.Fatal(err)
				}
				plan := previous.Plan
				plan.MaintenanceID, plan.RequestID, plan.LinkRequestID, plan.PreparationRevision = "new-capture", h.ids.New(protocol.PrefixRequest), h.ids.New(protocol.PrefixRequest), prep.Revision
				plan.Successor.UpgradeID, plan.Successor.ProcessInstanceID = "new-capture", "newer"
				process := protocol.RecoveryIdentity{PID: 124, StartTicks: 457, LaunchID: "newer", SHA256: plan.Successor.ArtifactSHA256}
				if _, err := h.app.RegisterCommittedMaintenance(ctx, plan, process); err != nil {
					t.Fatal(err)
				}
				proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: plan.MaintenanceID, RequestID: plan.RequestID, Previous: plan.Previous, Successor: plan.Successor, Process: process, Generation: 10, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
				if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
					t.Fatal(err)
				}
				if _, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
					t.Fatal(err)
				}
			} else if err := h.store.Write(ctx, func(tx *store.Tx) error {
				switch kind {
				case "new-pending":
					_, err := tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'turn','{}','unknown',?)`, h.ids.New(protocol.PrefixRequest), as.RunID, h.clock.Now().UnixMilli())
					return err
				default:
					_, err := tx.Exec(ctx, `UPDATE tasks SET control_revision=control_revision+1 WHERE task_id=?`, as.TaskID)
					return err
				}
			}); err != nil {
				t.Fatal(err)
			}
			beforeLease := h.lease(as.RunID)
			rt.Sequence++
			heartbeat, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: rt, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
			if err != nil || len(heartbeat.Runs) != 1 || heartbeat.Runs[0].Renewed || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
				t.Fatal("new state inherited old continuation permission", kind, heartbeat, err)
			}
			rt.Sequence++
			op, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
			if err != nil {
				t.Fatal(err)
			}
			var hello protocol.HelloResult
			if json.Unmarshal(op.Result, &hello) != nil || len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictRetain || len(hello.Reassigned) != 0 || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
				t.Fatal("new state reused the old original-journal authority", kind, op)
			}
			if kind == "new-capture" {
				s, err := h.app.RetainedRunReconciliation(ctx, as.RunID)
				if err != nil || len(s.Protections) != 2 || s.ProtectionSetSHA256 == p.ProtectionSetSHA256 || s.Protections[1].OriginalRequestsJSON != p.Protections[0].OriginalRequestsJSON {
					t.Fatal("new capture replaced or lost original protection facts", s, err)
				}
			}
		})
	}
}

func TestRetainedRunContinuationRejectsMutatedReplay(t *testing.T) {
	h, as, _ := ordinaryRetainedRunFixture(t)
	p := continuationPayload(t, h, as.RunID)
	request := h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(h.task(as.TaskID).ControlRevision))
	if op := h.exec(owner, request, ContinueRetainedRun{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p.Reason = "Different independent business action"
	request.Payload, _ = json.Marshal(p)
	op, err := h.app.Execute(t.Context(), owner, request, ContinueRetainedRun{})
	if protocol.CodeOf(err) != protocol.CodeStaleRevision && (op.Error == nil || op.Error.Code != protocol.CodeStaleRevision) {
		t.Fatal("mutated original request was accepted", op, err)
	}
}

func TestRetainedRunContinuationNegotiatesStrictVerdictForBothWireVersions(t *testing.T) {
	for _, wire := range []int64{protocol.PreviousRunnerProtocol, protocol.CurrentRunnerProtocol} {
		for _, strict := range []bool{false, true} {
			t.Run(fmt.Sprintf("wire-%d/strict-%t", wire, strict), func(t *testing.T) {
				h, as, rt := ordinaryRetainedRunFixture(t)
				p := continuationPayload(t, h, as.RunID)
				if op := h.exec(owner, h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, p, rev(h.task(as.TaskID).ControlRevision)), ContinueRetainedRun{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				rt.Protocol = wire
				rt.ProtocolMin, rt.ProtocolMax = wire, wire
				if !strict {
					rt.Capabilities = slices.DeleteFunc(rt.Capabilities, func(c string) bool { return c == protocol.CapabilityStrictRetainedResume })
				}
				op, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
				if err != nil {
					t.Fatal(err)
				}
				var hello protocol.HelloResult
				if json.Unmarshal(op.Result, &hello) != nil || len(hello.Runs) != 1 {
					t.Fatal(op)
				}
				v := hello.Runs[0]
				if strict && (v.Verdict != protocol.VerdictContinueRetained || v.RetainedResume == nil || v.RetainedResume.RequestID == "") {
					t.Fatal("strict peer did not receive its independent authorization", v)
				}
				if !strict && (v.Verdict != protocol.VerdictRetain || v.RetainedResume != nil) {
					t.Fatal("old peer received a new strict recovery directive", v)
				}
			})
		}
	}
}

func TestRetainedRunContinuationOwnerCannotControlAnotherIssue(t *testing.T) {
	var primary, other protocol.Issue
	h, as, _ := ordinaryRetainedRunFixture(t, func(h *harness) {
		if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		_, hosted := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
		h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(hosted))
		primary = assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
		other = assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
		p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: primary.AssignmentEpoch, Title: "Resume the original worker", Spec: testSpec}
		if op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: primary.IssueID}, p, rev(primary.Revision)), CanonicalOwnerTaskCreate{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	})
	p := continuationPayload(t, h, as.RunID)
	expected := h.task(as.TaskID).ControlRevision
	beforeLease := h.lease(as.RunID)
	foreignTurn, foreignPrincipal := startHostedIssueReview(t, h, other)
	op, err := h.app.Execute(t.Context(), foreignPrincipal, h.env(protocol.OpContinueRetainedRun, runTarget(as.RunID), p, rev(expected)), HostedCoordinationCommand{Command: ContinueRetainedRun{}, ExecutionID: foreignTurn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(err) != protocol.CodeForbidden || op.Status == protocol.OpSucceeded || h.task(as.TaskID).ControlRevision != expected {
		t.Fatal("foreign Issue owner changed original Run authority", op, err)
	}
	turn, principal := startHostedIssueReview(t, h, primary)
	op, err = h.app.Execute(t.Context(), principal, h.env(protocol.OpContinueRetainedRun, runTarget(as.RunID), p, rev(expected)), HostedCoordinationCommand{Command: ContinueRetainedRun{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if err != nil || op.Status != protocol.OpSucceeded || h.task(as.TaskID).ControlRevision != expected+1 || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("current Issue owner could not authorize exact original continuation", op, err)
	}
}

func TestRetainedRunContinuationMainRequiresAdmittedSource(t *testing.T) {
	h, as, _ := ordinaryRetainedRunFixture(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, hosted := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(hosted))
	_, delivery := h.say("Independently reconcile and attach the original retained worker")
	turn := startHostedReview(t, h, "main", delivery)
	p := continuationPayload(t, h, as.RunID)
	p.SourceMessageID, p.ExpectedSourceRevision = turn.MessageID, rev(1)
	expected := h.task(as.TaskID).ControlRevision
	for n, kind := range []string{"missing", "wrong-revision", "not-admitted", "valid"} {
		candidate := p
		switch kind {
		case "missing":
			candidate.SourceMessageID, candidate.ExpectedSourceRevision = "", nil
		case "wrong-revision":
			candidate.ExpectedSourceRevision = rev(2)
		case "not-admitted":
			candidate.SourceMessageID = queueMessage(t, h, "A queued input is not continuation authority")
		}
		op, err := h.app.Execute(t.Context(), mainAgent, h.env(protocol.OpContinueRetainedRun, protocol.Target{Type: "run", ID: as.RunID}, candidate, rev(expected)), HostedCoordinationCommand{Command: ContinueRetainedRun{}, ExecutionID: turn.RequestID, EffectSeq: int64(n + 1)})
		if kind == "valid" {
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal("admitted Main source could not authorize exact continuation", op, err)
			}
		} else if err == nil && op.Status == protocol.OpSucceeded {
			t.Fatal("unadmitted source authorized continuation", kind, op)
		}
	}
}
