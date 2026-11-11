//go:build linux

package upgradectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestCommittedMaintenanceActualProcessHealthAndLinkReplay(t *testing.T) {
	if os.Getenv("TEND_MAINTENANCE_HEALTH_TEST") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCommittedMaintenanceActualProcessHealthAndLinkReplay$")
		cmd.Env = append(os.Environ(), "TEND_MAINTENANCE_HEALTH_TEST=1", "TEND_UPDATER_LAUNCH_ID=maintenance-successor-test")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated actual-process test: %v\n%s", err, raw)
		}
		return
	}
	ctx := t.Context()
	root, err := os.MkdirTemp("", "tend-mh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	db, err := store.Open(ctx, filepath.Join(root, "ledger.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := protocol.NewULIDGen(time.Now, nil)
	initial := app.New(db, ids, time.Now)
	prep, err := initial.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "maintenance-health", RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{UpgradeID: "previous-committed", InstanceID: "server", ProcessInstanceID: "old-launch", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "maintenance-health", InstanceID: "server", ProcessInstanceID: actual.LaunchID, ArtifactSHA256: actual.SHA256, ReleaseID: "successor", ManifestSHA256: strings.Repeat("b", 64)}}
	a := app.New(db, ids, time.Now, app.WithCommittedMaintenanceStartup(p.MaintenanceID))
	socket := filepath.Join(root, "updater.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	target := updater.Version{Protocol: protocol.CurrentRunnerProtocol, StorageVersion: 81, ReleaseID: p.Successor.ReleaseID}
	state := updater.State{Format: 3, InstanceID: "server", Generation: 2, Maintenance: &updater.CommittedMaintenance{Plan: p, Phase: "starting_successor", Target: target}}
	acknowledgments := 0
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
		case "/maintenance/link":
			acknowledgments++
			var req updater.ControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.MaintenanceReceipt == nil || state.Maintenance.Receipt == nil || *req.MaintenanceReceipt != *state.Maintenance.Receipt || req.ExpectedGeneration != state.Generation {
				t.Error("acknowledgment changed receipt or CAS")
			}
			if acknowledgments == 1 {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(updater.ControlResponse{Error: "injected acknowledgment loss"})
				return
			}
			state.Maintenance.Phase = "ledger_linked"
		default:
			t.Error("unexpected external control", r.URL.Path)
		}
		json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	ready := false
	c, err := New(Config{Instance: "server", ProcessInstance: actual.LaunchID, BinarySHA256: actual.SHA256, ControlSocket: filepath.Join(root, "control.sock"), UpdaterSocket: socket, ReleaseDir: filepath.Join(root, "releases"), RecoveryReady: func(context.Context) error {
		if !ready {
			return errors.New("startup not ready")
		}
		return nil
	}}, a, ids, func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: false}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InitializeCommittedMaintenance(ctx, p.MaintenanceID); err != nil {
		t.Fatal(err)
	}
	h, err := c.maintenanceHealth(ctx, p)
	if err != nil || h.Ready || h.Sequence != 0 || !h.Registered || !h.Fenced {
		t.Fatal("unready candidate accepted", h, err)
	}
	wrong := p
	wrong.Successor.ArtifactSHA256 = strings.Repeat("c", 64)
	if _, err := c.maintenanceHealth(ctx, wrong); err == nil {
		t.Fatal("different target accepted")
	}
	ready = true
	first, err := c.maintenanceHealth(ctx, p)
	if err != nil || !first.Ready || first.Process != actual || first.Epoch != actual.LaunchID || first.Sequence == 0 {
		t.Fatal(first, err)
	}
	second, err := c.maintenanceHealth(ctx, p)
	if err != nil || !second.Ready || second.Sequence <= first.Sequence || second.Process != first.Process || second.Epoch != first.Epoch {
		t.Fatal(second, err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: actual, Generation: 3, Epoch: actual.LaunchID, FirstSequence: first.Sequence, SecondSequence: second.Sequence}
	state.Maintenance.Process = actual
	state.Maintenance.Phase = "successor_committed"
	state.Maintenance.Receipt = &proof
	state.Current = target
	state.Process = actual
	state.CommittedProof = &updater.CommittedProof{Binding: p.Successor, Version: target, Process: actual}
	if handled, err := c.maintenanceTick(ctx); !handled || err == nil {
		t.Fatal("lost acknowledgment not surfaced", handled, err)
	}
	row, _, err := a.CommittedMaintenanceReadiness(ctx, p.MaintenanceID)
	if err != nil || !row.Linked || row.Receipt == nil || *row.Receipt != proof {
		t.Fatal("durable DB receipt lost before acknowledgment", row, err)
	}
	if handled, err := c.maintenanceTick(ctx); !handled || err != nil {
		t.Fatal("same receipt did not reconcile", handled, err)
	}
	if acknowledgments != 2 {
		t.Fatal("unexpected acknowledgment count", acknowledgments)
	}
	if handled, err := c.maintenanceTick(ctx); handled || err != nil {
		t.Fatal("linked window still owns controller", handled, err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var rows, effects int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM server_committed_maintenances WHERE maintenance_id=? AND request_id=? AND linked=1`, p.MaintenanceID, p.RequestID).Scan(&rows); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE object_type='maintenance' AND object_id=? AND kind='committed_maintenance_linked'`, p.MaintenanceID).Scan(&effects); err != nil {
			return err
		}
		if rows != 1 || effects != 1 || row.Plan.LinkRequestID != p.LinkRequestID {
			t.Fatal("maintenance linkage duplicated or changed identity", rows, effects, row.Plan)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedMaintenanceStartupVerifiesFailedParentHistory(t *testing.T) {
	parent := protocol.CommittedMaintenancePlan{MaintenanceID: "failed-parent", PreparationRevision: 1, Successor: protocol.UpgradeBinding{UpgradeID: "failed-parent", InstanceID: "server", ProcessInstanceID: "failed-process", ArtifactSHA256: strings.Repeat("a", 64)}}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: parent.Successor.ProcessInstanceID, SHA256: parent.Successor.ArtifactSHA256}
	archive := protocol.ArchiveOnlineStatus{Preparation: protocol.ArchivePreparation{Prepared: true, Revision: 1}, HostedDrained: true}
	digest := func(v any) string {
		raw, _ := json.Marshal(v)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	child := protocol.CommittedMaintenancePlan{MaintenanceID: "repair", ParentMaintenanceID: parent.MaintenanceID, ParentPlan: &parent, ParentPlanSHA256: digest(parent), ParentArchiveSHA256: digest(archive), Previous: parent.Successor, PreviousProcess: process, PreparationRevision: 1}
	for _, phase := range []string{"checking_successor", "successor_committed", "ledger_linked"} {
		t.Run(phase, func(t *testing.T) {
			source := updater.CommittedMaintenance{Plan: parent, Process: process, Phase: phase, Irreversible: true, ArchiveAuthorization: &archive}
			if phase == "successor_committed" {
				source.Receipt = &protocol.CommittedMaintenanceReceipt{MaintenanceID: parent.MaintenanceID, Process: process, FirstSequence: 1, SecondSequence: 2}
			}
			observed := child
			observed.ParentReceipt = source.Receipt
			state := updater.State{MaintenanceHistory: map[string]updater.CommittedMaintenance{parent.MaintenanceID: source}}
			err := verifyMaintenanceParent(state, observed)
			if (err == nil) != (phase != "ledger_linked") {
				t.Fatal("failed-parent phase authorization", phase, err)
			}
			if phase == "ledger_linked" {
				return
			}
			if phase == "successor_committed" {
				wrongReceipt := observed
				wrongReceipt.ParentReceipt = nil
				if err := verifyMaintenanceParent(state, wrongReceipt); err == nil {
					t.Fatal("missing observed parent receipt accepted")
				}
				receipt := *observed.ParentReceipt
				receipt.SecondSequence++
				wrongReceipt.ParentReceipt = &receipt
				if err := verifyMaintenanceParent(state, wrongReceipt); err == nil {
					t.Fatal("changed observed parent receipt accepted")
				}
			}
			wrong := observed
			wrong.ParentPlanSHA256 = strings.Repeat("f", 64)
			if err := verifyMaintenanceParent(state, wrong); err == nil {
				t.Fatal("changed parent plan accepted")
			}
			wrong = observed
			wrong.PreviousProcess.StartTicks++
			if err := verifyMaintenanceParent(state, wrong); err == nil {
				t.Fatal("changed physical parent accepted")
			}
			tampered := archive
			tampered.Preparation.Revision++
			source.ArchiveAuthorization = &tampered
			state.MaintenanceHistory[parent.MaintenanceID] = source
			if err := verifyMaintenanceParent(state, observed); err == nil {
				t.Fatal("changed preparation witness accepted")
			}
		})
	}
	if err := verifyMaintenanceParent(updater.State{}, child); err == nil {
		t.Fatal("missing parent history invented")
	}
}
