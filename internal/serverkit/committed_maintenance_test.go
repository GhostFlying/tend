//go:build linux

package serverkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

// This test runs the actual production assembly, mainthread managers and local
// control handler. Only the updater transport and external session opener are
// replaced; hosted draining and startup readiness are never stubbed true.
func TestCommittedMaintenanceProductionAssembly(t *testing.T) {
	if os.Getenv("TEND_MAINTENANCE_ASSEMBLY_TEST") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCommittedMaintenanceProductionAssembly$", "-test.v")
		cmd.Env = append(os.Environ(), "TEND_MAINTENANCE_ASSEMBLY_TEST=1", "TEND_UPDATER_LAUNCH_ID=maintenance-assembly-process", "TEND_UPDATER_PROCESS_INSTANCE_ID=maintenance-assembly-process", "TEND_FORWARD_RECOVERY_ID=", "TEND_FORWARD_RECOVERY_SOCKET=")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("production assembly subprocess: %v\n%s", err, raw)
		}
		t.Log(string(raw))
		return
	}
	testCommittedMaintenanceProductionAssembly(t, false)
}

func TestReceiptRecoveryStartupProductionAssembly(t *testing.T) {
	if os.Getenv("TEND_RECEIPT_RECOVERY_ASSEMBLY_TEST") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestReceiptRecoveryStartupProductionAssembly$", "-test.v")
		cmd.Env = append(os.Environ(), "TEND_RECEIPT_RECOVERY_ASSEMBLY_TEST=1", "TEND_UPDATER_LAUNCH_ID=maintenance-assembly-process", "TEND_UPDATER_PROCESS_INSTANCE_ID=maintenance-assembly-process", "TEND_FORWARD_RECOVERY_ID=", "TEND_FORWARD_RECOVERY_SOCKET=")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("receipt recovery assembly subprocess: %v\n%s", err, raw)
		}
		t.Log(string(raw))
		return
	}
	testCommittedMaintenanceProductionAssembly(t, true)
}

func testCommittedMaintenanceProductionAssembly(t *testing.T, receiptRecovery bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "tend-ma-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	ids := protocol.NewULIDGen(time.Now, nil)
	db, err := store.Open(ctx, filepath.Join(data, "tend.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	seed := app.New(db, ids, time.Now)
	prep, err := seed.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	taskID, actionID, runID := ids.New(protocol.PrefixTask), ids.New(protocol.PrefixAction), ids.New(protocol.PrefixRun)
	retained := store.RunRow{Protocol: protocol.CurrentRunnerProtocol, RunID: runID, TaskID: taskID, ActionID: actionID, Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredRun, ObservedState: protocol.RunUnknown}
	if receiptRecovery {
		retained.HostID = "retained-host"
	}
	pin := protocol.RunExecutionPin{SnapshotID: "retained-snapshot", AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "retained-worker", Revision: 1}, Profile: protocol.ExecutionProfile{Model: "retained-model", Effort: "high"}}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if receiptRecovery {
			if err := tx.UpsertHost(ctx, store.HostRow{HostID: retained.HostID, OS: "linux", Arch: "amd64", Slots: 1}, 0); err != nil {
				return err
			}
		}
		if err := tx.InsertAgentCategory(ctx, protocol.AgentCategory{CategoryID: "planning", Revision: 1, Title: "Planning", State: "active"}, ids.New(protocol.PrefixRequest)); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, store.TaskRow{TaskID: taskID, Title: "retained unknown", Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, store.ActionRow{ActionID: actionID, TaskID: taskID, Kind: protocol.ActionImplement, EffectKey: "retained-effect", TriggerJSON: `{}`, AuthorizationJSON: `{}`, TargetSpecVersion: 1, Generation: 1, Status: protocol.ActionDispatched}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, retained); err != nil {
			return err
		}
		if receiptRecovery {
			if err := tx.InsertLease(ctx, store.LeaseRow{LeaseID: ids.New(protocol.PrefixLease), RunID: runID, HostID: retained.HostID, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, 0); err != nil {
				return err
			}
		}
		if err := tx.InsertRunExecution(ctx, runID, pin); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,created_at) VALUES('startup-evidence',0)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,created_at,updated_at) VALUES('startup-compaction','startup-evidence','unknown','manual',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if receiptRecovery {
		if err := db.Write(ctx, func(tx *store.Tx) error {
			message, request := ids.New(protocol.PrefixMessage), ids.New(protocol.PrefixRequest)
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: "main", Author: "owner", Body: "retained uncertain native history"}); err != nil {
				return err
			}
			if err := tx.PutMainThread(ctx, "main", "original-main", 0); err != nil {
				return err
			}
			if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: request, MessageID: message, ThreadID: "original-main", TurnID: "original-turn", Status: store.TurnInterrupted}, 0); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, request)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var schema int64
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	plan := protocol.CommittedMaintenancePlan{MaintenanceID: "assembly-maintenance", RequestID: ids.New(protocol.PrefixRequest), LinkRequestID: ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{UpgradeID: "previous", InstanceID: "server", ProcessInstanceID: "previous-process", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "assembly-maintenance", InstanceID: "server", ProcessInstanceID: actual.LaunchID, ArtifactSHA256: actual.SHA256, ManifestSHA256: strings.Repeat("b", 64), ReleaseID: "assembly-successor"}}
	if receiptRecovery {
		plan.ReceiptRecoveryStop = &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: taskID, Run: protocol.RunView{RunID: runID, Role: retained.Role, Generation: 1, SpecVersion: 1, Host: retained.HostID, Observed: retained.ObservedState}}}}
		plan.HostControls = []protocol.MaintenanceHostControl{{HostID: retained.HostID, Revision: 1, PreviousMode: protocol.RunnerMaintenance}}
	}
	target := updater.Version{Protocol: protocol.CurrentRunnerProtocol, StorageVersion: schema, ReleaseID: plan.Successor.ReleaseID}
	state := updater.State{Format: 3, InstanceID: "server", Generation: 7, Maintenance: &updater.CommittedMaintenance{Plan: plan, Process: actual, Phase: "checking_successor", Target: target}}
	if receiptRecovery {
		state.Maintenance.ReceiptRecoveryAuthorized = true
		state.Maintenance.ReceiptRecoveryRequestID = ids.New(protocol.PrefixRequest)
		raw, err := json.Marshal(plan.ReceiptRecoveryStop.Runs)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		state.Maintenance.ReceiptRecoveryFirstSHA256 = hex.EncodeToString(hash[:])
		state.Maintenance.ReceiptRecoverySecondSHA256 = state.Maintenance.ReceiptRecoveryFirstSHA256
		state.Maintenance.ReceiptRecoveryAuthorization = &protocol.MaintenanceReceiptRecoveryRequest{RequestID: state.Maintenance.ReceiptRecoveryRequestID, ExpectedGeneration: 6, FirstCapture: plan.ReceiptRecoveryStop.Runs, SecondCapture: plan.ReceiptRecoveryStop.Runs}
		state.Maintenance.ArchiveAuthorization = &protocol.ArchiveOnlineStatus{Preparation: prep, HostedDrained: false, Blockers: []string{"native receipt reads: 1", "hosted operations are active or uncertain"}, Hosts: []protocol.ArchiveHostStatus{{HostID: retained.HostID, OpenRuns: 1, Fresh: true, Control: protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: 1}, Runtime: &protocol.RunnerRuntime{Mode: protocol.RunnerMaintenance, AppliedControlRevision: 1}}}}
	}
	var stateMu sync.Mutex
	updaterSocket, controlSocket := filepath.Join(root, "updater.sock"), filepath.Join(root, "control.sock")
	ln, err := net.Listen("unix", updaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	acknowledged := make(chan struct{}, 1)
	fakeUpdater := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stateMu.Lock()
		defer stateMu.Unlock()
		if r.URL.Path == "/maintenance/link" {
			var req updater.ControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.MaintenanceReceipt == nil || state.Maintenance.Receipt == nil || *req.MaintenanceReceipt != *state.Maintenance.Receipt || req.ExpectedGeneration != state.Generation {
				t.Error("link acknowledgment changed receipt or CAS")
			}
			state.Maintenance.Phase = "ledger_linked"
			select {
			case acknowledged <- struct{}{}:
			default:
			}
		} else if r.URL.Path != "/status" {
			t.Error("unexpected updater mutation", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	served := make(chan struct{})
	go func() { defer close(served); _ = fakeUpdater.Serve(ln) }()
	defer func() { _ = fakeUpdater.Close(); <-served }()
	t.Setenv("TEND_COMMITTED_MAINTENANCE_ID", plan.MaintenanceID)
	t.Setenv("TEND_COMMITTED_MAINTENANCE_SOCKET", updaterSocket)
	named := hostedconfig.Configuration{Revision: 1, EnvironmentID: "fixture", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "unused-external-adapter", DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "fixture-model", Effort: "high", Enabled: true}}}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int64
	configured, initialized := false, false
	finished := errors.New("production assembly verified")
	localClient := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", controlSocket)
	}}}
	defer localClient.CloseIdleConnections()
	request := func(method, path string, input, out any) error {
		var body io.Reader
		if input != nil {
			raw, err := json.Marshal(input)
			if err != nil {
				return err
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, body)
		if err != nil {
			return err
		}
		resp, err := localClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("local %s: status %d", path, resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	cfg := Config{DataDir: data, MainAgent: &MainAgentConfig{Named: &named}, Upgrades: &UpgradeConfig{Instance: "server", ControlSocket: controlSocket, UpdaterSocket: updaterSocket, ReleaseDir: filepath.Join(root, "releases")}}
	err = Run(ctx, cfg, []byte("isolated-assembly-secret"), Dependencies{
		Listeners: []net.Listener{listener},
		OpenSession: func(context.Context, SessionSpec) (harness.Session, error) {
			opens.Add(1)
			return nil, errors.New("no model operation is needed while prepared")
		},
		Configure: func(ctx context.Context, a *app.App, _ protocol.IDGen) error {
			configured = true
			if _, err := a.BeginOwnerMutation(ctx); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				return errors.New("construction did not fence owner admission")
			}
			if _, _, err := a.CommittedMaintenanceReadiness(ctx, plan.MaintenanceID); !errors.Is(err, store.ErrNotFound) {
				return errors.New("maintenance row existed before startup binding")
			}
			return nil
		},
		Initialize: func(ctx context.Context, r *Runtime) error {
			initialized = true
			row, _, err := r.App.CommittedMaintenanceReadiness(ctx, plan.MaintenanceID)
			if err != nil {
				return err
			}
			if row.Linked || row.Process != actual {
				return errors.New("registration not bound before serving")
			}
			return nil
		},
		Background: func(ctx context.Context, r *Runtime) error {
			var health protocol.CommittedMaintenanceHealth
			for ctx.Err() == nil {
				if err := request(http.MethodPost, "/updater/maintenance/health", plan, &health); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if health.Ready || health.Sequence != 0 || !health.Registered || !health.Fenced {
				return fmt.Errorf("real startup readiness ignored uncertain compaction: %+v", health)
			}
			var archive protocol.ArchiveOnlineStatus
			if err := request(http.MethodGet, "/archive/status", nil, &archive); err != nil {
				return err
			}
			if !archive.Preparation.Prepared || archive.ActiveOwnerRequests != 0 || !archive.HostedDrained {
				return fmt.Errorf("real mainthread prepared drain: %+v", archive)
			}
			// The injected external completion resolves only the seeded startup fact.
			// Production readiness, managers, and all retained business rows stay real.
			if err := r.Store.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE agent_compactions SET status='completed' WHERE operation_id='startup-compaction'`)
				return err
			}); err != nil {
				return err
			}
			for ctx.Err() == nil {
				if err := request(http.MethodPost, "/updater/maintenance/health", plan, &health); err == nil && health.Ready {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			first := health
			if receiptRecovery && opens.Load() != 0 {
				return errors.New("native receipt process started before maintenance DB link")
			}
			if err := request(http.MethodPost, "/updater/maintenance/health", plan, &health); err != nil {
				return err
			}
			if !health.Ready || health.Sequence <= first.Sequence || health.Process != actual || health.Epoch != actual.LaunchID {
				return fmt.Errorf("real assembly health samples differ: %+v %+v", first, health)
			}
			proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: plan.MaintenanceID, RequestID: plan.RequestID, Previous: plan.Previous, Successor: plan.Successor, Process: actual, Generation: 8, Epoch: health.Epoch, FirstSequence: first.Sequence, SecondSequence: health.Sequence}
			stateMu.Lock()
			state.Maintenance.Phase = "successor_committed"
			state.Maintenance.Receipt = &proof
			state.Current = target
			state.Process = actual
			state.CommittedProof = &updater.CommittedProof{Binding: plan.Successor, Version: target, Process: actual}
			stateMu.Unlock()
			select {
			case <-acknowledged:
			case <-ctx.Done():
				return ctx.Err()
			}
			var row protocol.CommittedMaintenanceLedger
			if err := request(http.MethodGet, "/upgrades/maintenance/status?maintenance_id="+plan.MaintenanceID, nil, &row); err != nil {
				return err
			}
			if !row.Linked || row.Receipt == nil || *row.Receipt != proof {
				return errors.New("real controller did not link independent DB receipt")
			}
			if receiptRecovery {
				for ctx.Err() == nil && opens.Load() == 0 {
					time.Sleep(10 * time.Millisecond)
				}
				if ctx.Err() != nil {
					return errors.New("receipt startup did not resume after DB linkage")
				}
			}
			if err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
				run, err := store.GetRun(ctx, tx.Q(), runID)
				if err != nil {
					return err
				}
				if run != retained {
					return errors.New("assembly changed unknown run")
				}
				saved, err := store.GetRunExecution(ctx, tx.Q(), runID)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(*saved, pin) {
					return errors.New("assembly changed retained pin")
				}
				return nil
			}); err != nil {
				return err
			}
			// Releasing the maintenance fence does not steal the explicit archive gate.
			if _, err := r.App.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
				return err
			}
			done, err := r.App.BeginOwnerMutation(ctx)
			if err != nil {
				return err
			}
			done()
			return finished
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, finished) || !configured || !initialized || (!receiptRecovery && opens.Load() != 0) || (receiptRecovery && opens.Load() == 0) {
		t.Fatalf("assembly result=%v configure=%v initialize=%v model_opens=%d", err, configured, initialized, opens.Load())
	}
}
