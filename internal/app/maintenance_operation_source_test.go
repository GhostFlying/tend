package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func operationMaintenanceFixture(t *testing.T) (*harness, protocol.CommittedMaintenancePlan, protocol.RecoveryIdentity, store.UpgradeRow) {
	t.Helper()
	h := newHarness(t)
	ctx := t.Context()
	prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	original := store.UpgradeRow{StartRequestID: h.ids.New(protocol.PrefixRequest), ParkIdleManagers: true, View: protocol.UpgradeView{UpgradeID: "unfinished-upgrade", ReleaseID: "candidate", Revision: 3, Status: "attention", Phase: "server_ready", Reason: "candidate health unconfirmed"}, ManifestSHA256: strings.Repeat("a", 64), ServerHash: strings.Repeat("b", 64), ServerInstance: "server", ServerProcess: "unfinished-launch", ServerGrantDeadline: 123, Deadline: 456}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.PutUpgrade(ctx, original); err != nil {
			return err
		}
		result, _ := json.Marshal(original.View)
		return tx.InsertRequest(ctx, store.RequestRecord{Domain: "system", RequestID: original.StartRequestID, Operation: "upgrades.start", PayloadHash: strings.Repeat("f", 64), PrincipalJSON: `{"kind":"system"}`, TraceID: h.ids.New(protocol.PrefixTrace), Status: protocol.OpSucceeded, ResultJSON: string(result)})
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "independent-replacement", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{UpgradeID: original.View.UpgradeID, ReleaseID: original.View.ReleaseID, InstanceID: original.ServerInstance, ProcessInstanceID: original.ServerProcess, ManifestSHA256: original.ManifestSHA256, ArtifactSHA256: original.ServerHash}, Successor: protocol.UpgradeBinding{UpgradeID: "independent-replacement", InstanceID: "server", ProcessInstanceID: "replacement-launch", ArtifactSHA256: strings.Repeat("c", 64)}, SourceOperation: &protocol.MaintenanceOperationSource{OperationID: original.View.UpgradeID, Kind: "upgrade", Phase: "checking", OperationSHA256: strings.Repeat("d", 64), LaunchID: original.ServerProcess, Protocol: 2, StorageVersion: 1, OriginalRequestID: original.StartRequestID, OriginalLedger: &original.View}, PreviousProcess: protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: original.ServerProcess, SHA256: original.ServerHash}, FailedProcessExited: true}
	process := protocol.RecoveryIdentity{PID: 124, StartTicks: 457, LaunchID: p.Successor.ProcessInstanceID, SHA256: p.Successor.ArtifactSHA256}
	return h, p, process, original
}

func TestOperationMaintenanceClosesOnlyInstallationResponsibility(t *testing.T) {
	h, p, process, original := operationMaintenanceFixture(t)
	ctx := t.Context()
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.GetUpgrade(ctx, original.View.UpgradeID)
	if err != nil || !reflect.DeepEqual(before, original) {
		t.Fatal("registration rewrote original attempt", before, err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TRIGGER reject_operation_link BEFORE INSERT ON events WHEN NEW.kind='committed_maintenance_linked' BEGIN SELECT RAISE(ABORT,'injected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err == nil {
		t.Fatal("failed transaction released responsibility")
	}
	before, err = h.app.GetUpgrade(ctx, original.View.UpgradeID)
	if err != nil || !reflect.DeepEqual(before, original) {
		t.Fatal("partial link rewrote original", before, err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, `DROP TRIGGER reject_operation_link`); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal("exact link replay", err)
	}
	after, err := h.app.GetUpgrade(ctx, original.View.UpgradeID)
	if err != nil || after.View.Status != "cancelled" || after.View.Revision != original.View.Revision+1 || after.ServerHealthy != 0 || after.ServerProcess != original.ServerProcess || after.ServerGrantDeadline != original.ServerGrantDeadline {
		t.Fatal("original health/identity rewritten or responsibility left active", after, err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		parked, err := store.ManagerUpgradeParked(ctx, tx.Q())
		if err != nil {
			return err
		}
		if parked {
			t.Fatal("superseded installation still parks main")
		}
		ids, err := store.ListCancelledUpgradeIDs(ctx, tx.Q())
		if err != nil {
			return err
		}
		if len(ids) != 0 {
			t.Fatal("superseded physical operation would receive staged cancellation", ids)
		}
		var raw string
		if err := tx.Q().QueryRowContext(ctx, `SELECT body_json FROM events WHERE kind='maintenance.source_operation_retained' AND object_id=?`, p.MaintenanceID).Scan(&raw); err != nil {
			return err
		}
		var value struct {
			Original store.UpgradeRow `json:"original"`
		}
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return err
		}
		if !reflect.DeepEqual(value.Original, original) {
			t.Fatal("original unknown not retained", value.Original)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOperationMaintenanceRejectsChangedAuthorityAndFreshCAS(t *testing.T) {
	for _, field := range []string{"request", "binding", "ledger", "phase", "missing_physical", "runner_candidate", "link_cas"} {
		t.Run(field, func(t *testing.T) {
			h, p, process, original := operationMaintenanceFixture(t)
			ctx := t.Context()
			switch field {
			case "request":
				p.SourceOperation.OriginalRequestID = h.ids.New(protocol.PrefixRequest)
			case "binding":
				p.Previous.ArtifactSHA256 = strings.Repeat("e", 64)
			case "ledger":
				p.SourceOperation.OriginalLedger.Revision++
			case "phase":
				p.SourceOperation.Phase = "committed"
			case "missing_physical":
				p.PreviousProcess = protocol.RecoveryIdentity{}
				p.FailedProcessExited = false
			case "runner_candidate":
				original.Hosts = []store.UpgradeHost{{HostID: "host", CandidateInstance: "unresolved-candidate"}}
				if err := h.store.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, original) }); err != nil {
					t.Fatal(err)
				}
			}
			_, err := h.app.RegisterCommittedMaintenance(ctx, p, process)
			if field != "link_cas" {
				if err == nil {
					t.Fatal("changed source authority accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			original.View.Revision++
			if err := h.store.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, original) }); err != nil {
				t.Fatal(err)
			}
			proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 1, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
			if _, err := h.app.LinkCommittedMaintenance(ctx, proof); protocol.CodeOf(err) != protocol.CodeStaleRevision {
				t.Fatal("changed original CAS accepted", err)
			}
		})
	}
}

func TestOperationMaintenanceRestartUsesOriginalBindingAndIndependentLaunch(t *testing.T) {
	h, p, process, original := operationMaintenanceFixture(t)
	p.SourceOperation.Kind = "restart"
	p.SourceOperation.Phase = "launch_exited"
	p.SourceOperation.OperationID = "independent-restart-attempt"
	p.SourceOperation.LaunchID = "failed-restart-launch"
	p.SourceOperation.OriginalLedger = nil
	p.SourceOperation.OriginalRequestID = ""
	p.SourceOperation.LaunchExit = &protocol.MaintenanceLaunchExitProof{PID: 223, LaunchID: p.SourceOperation.LaunchID, ArtifactSHA256: p.Previous.ArtifactSHA256, ExitCode: 17}
	p.PreviousProcess = protocol.RecoveryIdentity{}
	p.Successor.ArtifactSHA256 = p.Previous.ArtifactSHA256
	process.SHA256 = p.Successor.ArtifactSHA256
	if _, err := h.app.RegisterCommittedMaintenance(t.Context(), p, process); err != nil {
		t.Fatal("independent same-version restart recovery rejected", err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 1, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(t.Context(), proof); err != nil {
		t.Fatal(err)
	}
	read, err := h.app.GetUpgrade(t.Context(), original.View.UpgradeID)
	if err != nil || !reflect.DeepEqual(read, original) {
		t.Fatal("restart rewrote unrelated installation history", read, err)
	}
}
