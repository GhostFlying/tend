package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCommittedMaintenancePreservesUnknownRunAndLinksTwoIndependentWindows(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	r := testRuntime("retained-runtime")
	h.versionedHello("linux-1", r)
	h.createTask(owner, createTaskPayload{Title: "retained work", Spec: testSpec})
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: r}, nil)
	as, ok := h.claimWith("linux-1", env)
	if !ok {
		t.Fatal("claim missing")
	}
	pin := protocol.RunExecutionPin{SnapshotID: "retained-snapshot", AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "retained-worker", Revision: 1}, Profile: protocol.ExecutionProfile{Model: "retained-model", Effort: "high"}}
	old := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "old-201", ReleaseID: "old-release", Status: "completed", Phase: "completed", Revision: 8}, ServerHash: strings.Repeat("a", 64), ServerInstance: "server", ServerProcess: "historic-process"}
	historic := protocol.ForwardRecoveryLedger{Plan: protocol.ForwardRecoveryPlan{RecoveryID: "historic-recovery", RequestID: h.ids.New(protocol.PrefixRequest), Original: protocol.UpgradeBinding{UpgradeID: old.View.UpgradeID}}, Linked: true, Receipt: &protocol.RecoveryReceipt{RecoveryID: "historic-recovery", FirstSequence: 1, SecondSequence: 2}}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateRunObserved(ctx, as.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.InsertRunExecution(ctx, as.RunID, pin); err != nil {
			return err
		}
		if err := tx.PutUpgrade(ctx, old); err != nil {
			return err
		}
		return tx.PutForwardRecovery(ctx, historic, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	// Freeze the stored representation, including JSON null/empty normalization.
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		historic, err = store.GetForwardRecovery(ctx, tx.Q(), historic.Plan.RecoveryID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var runBefore store.RunRow
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		runBefore, err = store.GetRun(ctx, tx.Q(), as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "maintenance-first", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{UpgradeID: "historic-recovery", InstanceID: "server", ProcessInstanceID: "committed-process", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "maintenance-first", InstanceID: "server", ProcessInstanceID: "first-successor", ArtifactSHA256: strings.Repeat("b", 64)}}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: p.Successor.ProcessInstanceID, SHA256: p.Successor.ArtifactSHA256}
	h.app.committedMaintenanceID = p.MaintenanceID
	if _, err := h.app.BeginOwnerMutation(ctx); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("missing startup row did not fence", err)
	}
	// The original assignment remains replayable while a new claim is fenced.
	if replay, ok := h.claimWith("linux-1", env); !ok || !reflect.DeepEqual(replay, as) {
		t.Fatal("retained claim changed")
	}
	fresh := h.env(protocol.OpRunnerClaim, env.Target, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: r}, nil)
	if _, err := h.app.Execute(ctx, runner("linux-1"), fresh, Claim{}); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("new claim bypassed maintenance", err)
	}
	row, err := h.app.RegisterCommittedMaintenance(ctx, p, process)
	if err != nil || row.Linked {
		t.Fatal(row, err)
	}
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal("exact registration replay", err)
	}
	changed := p
	changed.Successor.ReleaseID = "changed"
	if _, err := h.app.RegisterCommittedMaintenance(ctx, changed, process); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("changed registration replay accepted", err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 10, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TRIGGER reject_maintenance_link BEFORE INSERT ON events WHEN NEW.kind='committed_maintenance_linked' BEGIN SELECT RAISE(ABORT,'injected linkage failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err == nil {
		t.Fatal("failed transaction released fence")
	}
	row, _, err = h.app.CommittedMaintenanceReadiness(ctx, p.MaintenanceID)
	if err != nil || row.Linked {
		t.Fatal(row, err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, `DROP TRIGGER reject_maintenance_link`); return err }); err != nil {
		t.Fatal(err)
	}
	first, err := h.app.LinkCommittedMaintenance(ctx, proof)
	if err != nil || !first.Linked {
		t.Fatal(first, err)
	}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal("exact linkage replay", err)
	}
	malformed := proof
	malformed.SecondSequence++
	if _, err := h.app.LinkCommittedMaintenance(ctx, malformed); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("changed receipt accepted", err)
	}
	// Explicitly finish the first preparation, then create a genuinely new window.
	cancelled, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false)
	if err != nil {
		t.Fatal(err)
	}
	prep, err = h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: cancelled.Revision}, true)
	if err != nil {
		t.Fatal(err)
	}
	next := p
	next.MaintenanceID = "maintenance-second"
	next.RequestID = h.ids.New(protocol.PrefixRequest)
	next.LinkRequestID = h.ids.New(protocol.PrefixRequest)
	next.PreparationRevision = prep.Revision
	next.Previous = p.Successor
	next.Successor = protocol.UpgradeBinding{UpgradeID: next.MaintenanceID, InstanceID: "server", ProcessInstanceID: "second-successor", ArtifactSHA256: strings.Repeat("c", 64)}
	nextProcess := protocol.RecoveryIdentity{PID: 124, StartTicks: 457, LaunchID: next.Successor.ProcessInstanceID, SHA256: next.Successor.ArtifactSHA256}
	h.app.committedMaintenanceID = next.MaintenanceID
	if _, err := h.app.RegisterCommittedMaintenance(ctx, next, nextProcess); err != nil {
		t.Fatal("second independent registration", err)
	}
	nextProof := protocol.CommittedMaintenanceReceipt{MaintenanceID: next.MaintenanceID, RequestID: next.RequestID, Previous: next.Previous, Successor: next.Successor, Process: nextProcess, Generation: 20, Epoch: nextProcess.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(ctx, nextProof); err != nil {
		t.Fatal("second independent linkage", err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		run, err := store.GetRun(ctx, tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(run, runBefore) {
			t.Fatal("unknown run changed", run)
		}
		retained, err := store.GetRunExecution(ctx, tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		if *retained != pin {
			t.Fatal("pin changed")
		}
		original, err := store.GetUpgrade(ctx, tx.Q(), old.View.UpgradeID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(original, old) {
			t.Fatal("old 201 changed")
		}
		recovery, err := store.GetForwardRecovery(ctx, tx.Q(), historic.Plan.RecoveryID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(recovery, historic) {
			t.Fatal("old recovery changed")
		}
		previous, err := store.GetCommittedMaintenance(ctx, tx.Q(), p.MaintenanceID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(previous, first) {
			t.Fatal("first window overwritten")
		}
		var count int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind='committed_maintenance_linked'`).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatal("linkage events duplicated", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedMaintenanceRejectsPreparationAndReceiptMismatch(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "maintenance", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: 1, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "maintenance", InstanceID: "server", ProcessInstanceID: "next", ArtifactSHA256: strings.Repeat("b", 64)}}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "next", SHA256: p.Successor.ArtifactSHA256}
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal("unprepared registration", err)
	}
	prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p.RequestID = h.ids.New(protocol.PrefixRequest)
	p.PreparationRevision = prep.Revision
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 1, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 1}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("one repeated health sample accepted", err)
	}
	row, _, err := h.app.CommittedMaintenanceReadiness(ctx, p.MaintenanceID)
	if err != nil || row.Linked || row.Receipt != nil {
		t.Fatal("invalid proof changed durable linkage", row, err)
	}
}

func TestCommittedMaintenanceFailedParentResponsibilityPassesWithoutRewritingHistory(t *testing.T) {
	for _, kind := range []string{"checking_registered", "checking_never_registered", "committed_ack_lost"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
			if err != nil {
				t.Fatal(err)
			}
			parent := protocol.CommittedMaintenancePlan{MaintenanceID: "failed-parent", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{UpgradeID: "prior-committed", InstanceID: "server", ProcessInstanceID: "prior-process", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "failed-parent", InstanceID: "server", ProcessInstanceID: "failed-process", ArtifactSHA256: strings.Repeat("b", 64)}}
			parentProcess := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: parent.Successor.ProcessInstanceID, SHA256: parent.Successor.ArtifactSHA256}
			var frozen protocol.CommittedMaintenanceLedger
			var parentProof protocol.CommittedMaintenanceReceipt
			var parentLinkRequest store.RequestRecord
			if kind != "checking_never_registered" {
				if _, err := h.app.RegisterCommittedMaintenance(ctx, parent, parentProcess); err != nil {
					t.Fatal(err)
				}
				if kind == "committed_ack_lost" {
					parentProof = protocol.CommittedMaintenanceReceipt{MaintenanceID: parent.MaintenanceID, RequestID: parent.RequestID, Previous: parent.Previous, Successor: parent.Successor, Process: parentProcess, Generation: 9, Epoch: parentProcess.LaunchID, FirstSequence: 1, SecondSequence: 2}
					if _, err := h.app.LinkCommittedMaintenance(ctx, parentProof); err != nil {
						t.Fatal(err)
					}
					if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
						var err error
						parentLinkRequest, err = store.GetRequest(ctx, tx.Q(), "system", parent.LinkRequestID)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}

				frozen, _, err = h.app.CommittedMaintenanceReadiness(ctx, parent.MaintenanceID)
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(parent)
			sum := sha256.Sum256(raw)
			child := protocol.CommittedMaintenancePlan{MaintenanceID: "repaired-child", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: parent.PreparationRevision, Previous: parent.Successor, PreviousProcess: parentProcess, Successor: protocol.UpgradeBinding{UpgradeID: "repaired-child", InstanceID: "server", ProcessInstanceID: "repaired-process", ArtifactSHA256: strings.Repeat("c", 64)}, ParentMaintenanceID: parent.MaintenanceID, ParentPlan: &parent, ParentPlanSHA256: hex.EncodeToString(sum[:]), ParentArchiveSHA256: strings.Repeat("d", 64), ExpectedGeneration: 10, ParentGeneration: 10}
			child.ParentReceipt = frozen.Receipt
			process := protocol.RecoveryIdentity{PID: 124, StartTicks: 457, LaunchID: child.Successor.ProcessInstanceID, SHA256: child.Successor.ArtifactSHA256}
			h.app.committedMaintenanceID = child.MaintenanceID
			if kind == "committed_ack_lost" {
				for _, mismatch := range []string{"missing", "changed"} {
					wrong := child
					wrong.RequestID = h.ids.New(protocol.PrefixRequest)
					if mismatch == "missing" {
						wrong.ParentReceipt = nil
					} else {
						receipt := *child.ParentReceipt
						receipt.SecondSequence++
						wrong.ParentReceipt = &receipt
					}
					if _, err := h.app.RegisterCommittedMaintenance(ctx, wrong, process); protocol.CodeOf(err) != protocol.CodeConflictPayload {
						t.Fatal("unconfirmed parent receipt accepted", mismatch, err)
					}
				}
			}
			if _, err := h.app.RegisterCommittedMaintenance(ctx, child, process); err != nil {
				t.Fatal("child registration blocked by retained failed parent", err)
			}
			if _, err := h.app.RegisterCommittedMaintenance(ctx, child, process); err != nil {
				t.Fatal("child exact registration replay", err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				fenced, err := store.CommittedMaintenanceFenced(ctx, tx.Q())
				if err == nil && !fenced {
					t.Fatal("new child did not retain its own admission fence")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: child.MaintenanceID, RequestID: child.RequestID, Previous: child.Previous, Successor: child.Successor, Process: process, Generation: 20, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
			if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
				t.Fatal(err)
			}
			if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
				t.Fatal("child exact link replay", err)
			}
			if kind != "checking_never_registered" {
				unchanged, _, err := h.app.CommittedMaintenanceReadiness(ctx, parent.MaintenanceID)
				if err != nil || !reflect.DeepEqual(unchanged, frozen) {
					t.Fatal("failed parent receipt/history changed", unchanged, err)
				}
				stale := protocol.CommittedMaintenanceReceipt{MaintenanceID: parent.MaintenanceID, RequestID: parent.RequestID, Previous: parent.Previous, Successor: parent.Successor, Process: parentProcess, Generation: 9, Epoch: parentProcess.LaunchID, FirstSequence: 1, SecondSequence: 2}
				if kind == "committed_ack_lost" {
					replay, err := h.app.LinkCommittedMaintenance(ctx, parentProof)
					if err != nil || !reflect.DeepEqual(replay, frozen) {
						t.Fatal("successful parent request no longer replays its real receipt", replay, err)
					}
					if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
						recorded, err := store.GetRequest(ctx, tx.Q(), "system", parent.LinkRequestID)
						if err == nil && !reflect.DeepEqual(recorded, parentLinkRequest) {
							t.Fatal("successful parent request record changed")
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
				} else if _, err := h.app.LinkCommittedMaintenance(ctx, stale); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
					t.Fatal("superseded parent released admission", err)
				}
			} else {
				if _, _, err := h.app.CommittedMaintenanceReadiness(ctx, parent.MaintenanceID); !errors.Is(err, store.ErrNotFound) {
					t.Fatal("parent registration was invented", err)
				}
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				fenced, err := store.CommittedMaintenanceFenced(ctx, tx.Q())
				if err == nil && fenced {
					t.Fatal("unlinked non-leaf parent permanently fenced repaired child")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			sibling := child
			sibling.MaintenanceID = "forked-child"
			sibling.RequestID = h.ids.New(protocol.PrefixRequest)
			sibling.LinkRequestID = h.ids.New(protocol.PrefixRequest)
			sibling.Successor.UpgradeID = sibling.MaintenanceID
			if _, err := h.app.RegisterCommittedMaintenance(ctx, sibling, process); protocol.CodeOf(err) != protocol.CodeConflictPayload {
				t.Fatal("failed parent granted another child", err)
			}
		})
	}
}
