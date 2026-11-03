//go:build linux

package upgradectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestReceiptRecoveryStartupRequiresExactPersistedAuthority(t *testing.T) {
	ids := protocol.NewULIDGen(time.Now, nil)
	p := protocol.CommittedMaintenancePlan{RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: 1, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{}}
	raw, err := json.Marshal(p.ReceiptRecoveryStop.Runs)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	sum := hex.EncodeToString(hash[:])
	request := ids.New(protocol.PrefixRequest)
	newSource := func() updater.CommittedMaintenance {
		return updater.CommittedMaintenance{Plan: p, ReceiptRecoveryAuthorized: true, ReceiptRecoveryRequestID: request, ReceiptRecoveryFirstSHA256: sum, ReceiptRecoverySecondSHA256: sum, ReceiptRecoveryAuthorization: &protocol.MaintenanceReceiptRecoveryRequest{RequestID: request, ExpectedGeneration: 1}, ArchiveAuthorization: &protocol.ArchiveOnlineStatus{Preparation: protocol.ArchivePreparation{Prepared: true, Revision: 1}, HostedDrained: false, Blockers: []string{"native receipt reads: 1", "hosted operations are active or uncertain"}}}
	}
	if err := verifyReceiptRecoveryAuthorization(newSource()); err != nil {
		t.Fatal("exact authority rejected", err)
	}
	for _, mutation := range []string{"missing_authority", "changed_request", "reused_request", "changed_capture", "changed_digest", "new_effect", "missing_pending", "ordinary_scope"} {
		t.Run(mutation, func(t *testing.T) {
			source := newSource()
			switch mutation {
			case "missing_authority":
				source.ReceiptRecoveryAuthorized = false
			case "changed_request":
				source.ReceiptRecoveryAuthorization.RequestID = ids.New(protocol.PrefixRequest)
			case "reused_request":
				source.ReceiptRecoveryRequestID = p.RequestID
			case "changed_capture":
				source.ReceiptRecoveryAuthorization.SecondCapture = []protocol.CommittedRetainedRun{{TaskID: "different"}}
			case "changed_digest":
				source.ReceiptRecoveryFirstSHA256 = hex.EncodeToString(make([]byte, 32))
				source.ReceiptRecoverySecondSHA256 = source.ReceiptRecoveryFirstSHA256
			case "new_effect":
				source.ArchiveAuthorization.Blockers = append(source.ArchiveAuthorization.Blockers, "native steers: 1")
			case "missing_pending":
				source.ArchiveAuthorization.Blockers = nil
			case "ordinary_scope":
				source.Plan.ReceiptRecoveryStop = nil
			}
			if err := verifyReceiptRecoveryAuthorization(source); err == nil {
				t.Fatal("unproven recovery authority accepted")
			}
		})
	}
}

func recoveryCaptureHash(t *testing.T, capture []protocol.CommittedRetainedRun) string {
	t.Helper()
	raw, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func changedRecoveryObservations(t *testing.T) updater.CommittedMaintenance {
	t.Helper()
	ids := protocol.NewULIDGen(time.Now, nil)
	entry := protocol.CommittedRetainedRun{TaskID: ids.New(protocol.PrefixTask), Run: protocol.RunView{RunID: ids.New(protocol.PrefixRun), Role: protocol.RoleImplement, Generation: 1, SpecVersion: 1, Host: "retained-host", Observed: protocol.RunRunning}}
	first := []protocol.CommittedRetainedRun{entry}
	second := []protocol.CommittedRetainedRun{entry}
	second[0].Run.Observed = protocol.RunUnknown
	second[0].Run.Preparation = &protocol.RunPreparationFailure{Phase: "workspace", Code: "original_preparation_observed", WorkspaceID: "retained-workspace"}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "changed-observations", RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: 1, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{entry}}, HostControls: []protocol.MaintenanceHostControl{{HostID: entry.Run.Host, PreviousMode: protocol.RunnerMaintenance, Revision: 1}}}
	request := ids.New(protocol.PrefixRequest)
	return updater.CommittedMaintenance{Plan: p, ReceiptRecoveryAuthorized: true, ReceiptRecoveryRequestID: request, ReceiptRecoveryAuthorization: &protocol.MaintenanceReceiptRecoveryRequest{RequestID: request, ExpectedGeneration: 2, FirstCapture: first, SecondCapture: second}, ReceiptRecoveryFirstSHA256: recoveryCaptureHash(t, first), ReceiptRecoverySecondSHA256: recoveryCaptureHash(t, second), ArchiveAuthorization: &protocol.ArchiveOnlineStatus{Preparation: protocol.ArchivePreparation{Prepared: true, Revision: 1}, HostedDrained: true, Blockers: []string{"native receipt reads: 1"}, Hosts: []protocol.ArchiveHostStatus{{HostID: entry.Run.Host, Fresh: true, OpenRuns: 1, Control: protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: 1}, Runtime: &protocol.RunnerRuntime{Mode: protocol.RunnerMaintenance, AppliedControlRevision: 1}}}}}
}

func TestReceiptRecoveryStartupAcceptsDistinctCompleteObservations(t *testing.T) {
	for _, drained := range []bool{false, true} {
		source := changedRecoveryObservations(t)
		source.ArchiveAuthorization.HostedDrained = drained
		if !drained {
			source.ArchiveAuthorization.Blockers = append(source.ArchiveAuthorization.Blockers, "hosted operations are active or uncertain")
		}
		if source.ReceiptRecoveryFirstSHA256 == source.ReceiptRecoverySecondSHA256 {
			t.Fatal("fixture lacks distinct complete observations")
		}
		before, _ := json.Marshal(source)
		if err := verifyReceiptRecoveryAuthorization(source); err != nil {
			t.Fatal("same retained identities rejected", drained, err)
		}
		after, _ := json.Marshal(source)
		if string(before) != string(after) {
			t.Fatal("startup rewrote original observations or their hashes")
		}
	}
	for _, change := range []string{"missing", "new", "duplicate", "task", "host", "role", "generation", "spec", "terminal", "changed_raw_hash", "swapped_hashes"} {
		t.Run(change, func(t *testing.T) {
			source := changedRecoveryObservations(t)
			second := &source.ReceiptRecoveryAuthorization.SecondCapture
			switch change {
			case "missing":
				*second = nil
			case "new":
				extra := (*second)[0]
				extra.Run.RunID = protocol.NewULIDGen(time.Now, nil).New(protocol.PrefixRun)
				*second = append(*second, extra)
			case "duplicate":
				*second = append(*second, (*second)[0])
			case "task":
				(*second)[0].TaskID = protocol.NewULIDGen(time.Now, nil).New(protocol.PrefixTask)
			case "host":
				(*second)[0].Run.Host = "rebound-host"
			case "role":
				(*second)[0].Run.Role = protocol.RoleAccept
			case "generation":
				(*second)[0].Run.Generation++
			case "spec":
				(*second)[0].Run.SpecVersion++
			case "terminal":
				(*second)[0].Run.Observed = protocol.RunFinished
			case "changed_raw_hash":
				(*second)[0].Run.Preparation.Code = "mutated_without_updating_original_proof"
			case "swapped_hashes":
				source.ReceiptRecoveryFirstSHA256, source.ReceiptRecoverySecondSHA256 = source.ReceiptRecoverySecondSHA256, source.ReceiptRecoveryFirstSHA256
			}
			if change != "changed_raw_hash" && change != "swapped_hashes" {
				source.ReceiptRecoverySecondSHA256 = recoveryCaptureHash(t, *second)
			}
			if err := verifyReceiptRecoveryAuthorization(source); err == nil {
				t.Fatal("unproven original inventory accepted")
			}
		})
	}
}

// Exercise actual successor initialization, including its real kernel identity,
// Unix updater status, app registration transaction and original lease capture.
// The previous service is absent; no pre-stop API can rescue a rejected startup.
func TestReceiptRecoveryChangedObservationsActualSuccessorStartup(t *testing.T) {
	if os.Getenv("TEND_CHANGED_RECOVERY_STARTUP_TEST") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestReceiptRecoveryChangedObservationsActualSuccessorStartup$")
		cmd.Env = append(os.Environ(), "TEND_CHANGED_RECOVERY_STARTUP_TEST=1", "TEND_UPDATER_LAUNCH_ID=changed-recovery-successor")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("actual successor startup: %v\n%s", err, raw)
		}
		return
	}
	ctx := t.Context()
	root, err := os.MkdirTemp("", "tend-rs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
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
	source := changedRecoveryObservations(t)
	source.Plan.PreparationRevision = prep.Revision
	source.Plan.Previous = protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}
	source.Plan.Successor = protocol.UpgradeBinding{UpgradeID: source.Plan.MaintenanceID, InstanceID: "server", ProcessInstanceID: actual.LaunchID, ArtifactSHA256: actual.SHA256}
	source.Phase = "starting_successor"
	source.Process = actual
	entry := source.Plan.ReceiptRecoveryStop.Runs[0]
	actionID := ids.New(protocol.PrefixAction)
	leaseID := ids.New(protocol.PrefixLease)
	original := store.RunRow{RunID: entry.Run.RunID, TaskID: entry.TaskID, ActionID: actionID, HostID: entry.Run.Host, Role: entry.Run.Role, Generation: 1, Protocol: protocol.CurrentRunnerProtocol, DesiredState: protocol.DesiredRun, ObservedState: protocol.RunUnknown}
	pin := protocol.RunExecutionPin{SnapshotID: "original-snapshot", AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "original-runtime", Revision: 1}}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if err := tx.UpsertHost(ctx, store.HostRow{HostID: entry.Run.Host, OS: "linux", Arch: "amd64", Slots: 1}, 0); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, store.TaskRow{TaskID: entry.TaskID, Title: "retained unknown", Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, store.ActionRow{ActionID: actionID, TaskID: entry.TaskID, Kind: protocol.ActionImplement, EffectKey: "original-effect", Generation: 1, TargetSpecVersion: 1, Status: protocol.ActionDispatched, TriggerJSON: `{}`, AuthorizationJSON: `{}`}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, original); err != nil {
			return err
		}
		if err := tx.InsertLease(ctx, store.LeaseRow{LeaseID: leaseID, RunID: original.RunID, HostID: original.HostID, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, 0); err != nil {
			return err
		}
		return tx.InsertRunExecution(ctx, original.RunID, pin)
	}); err != nil {
		t.Fatal(err)
	}
	state := updater.State{Format: 3, InstanceID: "server", Generation: 3, Maintenance: &source}
	socket := filepath.Join(root, "updater.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Error("unexpected previous-service control", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	a := app.New(db, ids, time.Now, app.WithCommittedMaintenanceStartup(source.Plan.MaintenanceID))
	c, err := New(Config{Instance: "server", ProcessInstance: actual.LaunchID, BinarySHA256: actual.SHA256, ControlSocket: filepath.Join(root, "control.sock"), UpdaterSocket: socket, ReleaseDir: filepath.Join(root, "releases")}, a, ids, func(context.Context) (app.ServerObservation, error) { return app.ServerObservation{}, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InitializeCommittedMaintenance(ctx, source.Plan.MaintenanceID); err != nil {
		t.Fatal("same execution observations blocked actual successor startup", err)
	}
	if pending, err := a.ReceiptRecoveryStartupPending(ctx); err != nil || !pending {
		t.Fatal("startup fence missing", pending, err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		protected, err := store.MaintenanceProtectedRuns(ctx, tx.Q(), original.RunID)
		if err != nil {
			return err
		}
		if len(protected) != 1 || protected[0].LeaseID != leaseID || protected[0].ActionID != actionID {
			t.Fatal("original lease protection was not registered", protected)
		}
		run, err := store.GetRun(ctx, tx.Q(), original.RunID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(run, original) {
			t.Fatal("startup changed original business run", run)
		}
		saved, err := store.GetRunExecution(ctx, tx.Q(), original.RunID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(saved, &pin) {
			t.Fatal("startup changed original pin")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptRecoveryLaunchExitParentRequiresExactOwnedHistory(t *testing.T) {
	newSource := func() (updater.State, protocol.CommittedMaintenancePlan) {
		parent := changedRecoveryObservations(t)
		parent.Plan.Previous = protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "prior-committed", ArtifactSHA256: strings.Repeat("a", 64)}
		parent.Plan.Successor = protocol.UpgradeBinding{UpgradeID: parent.Plan.MaintenanceID, InstanceID: "server", ProcessInstanceID: "fast-exit-launch", ArtifactSHA256: strings.Repeat("b", 64)}
		parent.Phase = "launch_exited"
		parent.Irreversible = true
		parent.LaunchExit = &protocol.MaintenanceLaunchExitProof{PID: 123, LaunchID: parent.Plan.Successor.ProcessInstanceID, ArtifactSHA256: parent.Plan.Successor.ArtifactSHA256, ExitCode: 2}
		planRaw, _ := json.Marshal(parent.Plan)
		planHash := sha256.Sum256(planRaw)
		archiveRaw, _ := json.Marshal(parent.ArchiveAuthorization)
		archiveHash := sha256.Sum256(archiveRaw)
		proof := *parent.LaunchExit
		child := protocol.CommittedMaintenancePlan{MaintenanceID: "repair-fast-exit", ParentMaintenanceID: parent.Plan.MaintenanceID, ParentPlan: &parent.Plan, ParentPlanSHA256: hex.EncodeToString(planHash[:]), ParentArchiveSHA256: hex.EncodeToString(archiveHash[:]), ParentLaunchExit: &proof, FailedProcessExited: true, PreparationRevision: parent.Plan.PreparationRevision, Previous: parent.Plan.Successor, ReceiptRecoveryStop: parent.Plan.ReceiptRecoveryStop}
		return updater.State{MaintenanceHistory: map[string]updater.CommittedMaintenance{parent.Plan.MaintenanceID: parent}}, child
	}
	state, child := newSource()
	if err := verifyMaintenanceParent(state, child); err != nil {
		t.Fatal("real launch exit provenance rejected", err)
	}
	for _, mutation := range []string{"missing_proof", "changed_pid", "changed_launch", "changed_artifact", "changed_wait_status", "invented_process", "missing_exited_intent", "missing_history_proof", "missing_irreversible", "unknown_phase", "missing_wait_status"} {
		t.Run(mutation, func(t *testing.T) {
			state, child := newSource()
			parent := state.MaintenanceHistory[child.ParentMaintenanceID]
			switch mutation {
			case "missing_proof":
				child.ParentLaunchExit = nil
			case "changed_pid":
				child.ParentLaunchExit.PID++
			case "changed_launch":
				child.ParentLaunchExit.LaunchID = "another-launch"
			case "changed_artifact":
				child.ParentLaunchExit.ArtifactSHA256 = strings.Repeat("d", 64)
			case "changed_wait_status":
				child.ParentLaunchExit.ExitCode++
			case "invented_process":
				child.PreviousProcess = protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: parent.Plan.Successor.ProcessInstanceID, SHA256: parent.Plan.Successor.ArtifactSHA256}
			case "missing_exited_intent":
				child.FailedProcessExited = false
			case "missing_history_proof":
				parent.LaunchExit = nil
			case "missing_irreversible":
				parent.Irreversible = false
			case "unknown_phase":
				parent.Phase = "starting_successor"
			case "missing_wait_status":
				parent.LaunchExit.ExitCode = -1
				child.ParentLaunchExit.ExitCode = -1
			}
			state.MaintenanceHistory[child.ParentMaintenanceID] = parent
			if err := verifyMaintenanceParent(state, child); err == nil {
				t.Fatal("unproven launch exit authorized a successor")
			}
		})
	}
}
