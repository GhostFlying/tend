package runner

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestHeartbeatReportsNoStartForCancelledBatchOverPriorCommit(t *testing.T) {
	for _, kind := range []string{"active-commit-with-old-cancel", "staged-cas", "current-batch", "cancelled-id", "incomplete-tombstone", "unrelated-batch", "wrong-current-process"} {
		t.Run(kind, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
			r.cfg.Log = slog.Default()
			r.c.log = r.cfg.Log
			ctx := t.Context()
			if err := r.loadControl(ctx); err != nil {
				t.Fatal(err)
			}
			old := updater.Version{ReleaseID: "old", ManifestSHA256: strings.Repeat("a", 64), Artifact: release.Artifact{SHA256: strings.Repeat("b", 64)}}
			current := updater.Identity{PID: 7, StartTicks: 1, LaunchID: r.instanceID, SHA256: old.Artifact.SHA256}
			oldBinding := protocol.UpgradeBinding{UpgradeID: "previous", InstanceID: "host", ProcessInstanceID: "old-candidate", ReleaseID: old.ReleaseID, ManifestSHA256: old.ManifestSHA256, ArtifactSHA256: old.Artifact.SHA256}
			binding := protocol.UpgradeBinding{UpgradeID: "batch", InstanceID: "host", ProcessInstanceID: "never-started-candidate", ReleaseID: "new", ManifestSHA256: strings.Repeat("c", 64), ArtifactSHA256: strings.Repeat("d", 64)}
			state := updater.State{InstanceID: "host", Current: old, Process: current, CommittedProof: &updater.CommittedProof{Binding: oldBinding, Version: old, Process: current}, Terminals: map[string]updater.TerminalReceipt{"batch": {Binding: binding, Phase: "cancelled"}}}
			r.cfg.Build.BinarySHA256 = current.SHA256
			r.cfg.UpdaterInstanceID = "host"
			r.control = protocol.RunnerControl{Revision: 3, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", CancelledUpgradeID: "batch"}
			var stateMu sync.Mutex
			cancelCalls := 0
			switch kind {
			case "active-commit-with-old-cancel":
				r.control.UpgradeID = oldBinding.UpgradeID
			case "staged-cas":
				delete(state.Terminals, "batch")
				state.Operation = &updater.Operation{ID: "batch", Kind: "upgrade", Phase: "staged", Binding: binding}
			case "cancelled-id":
				r.control.UpgradeID = ""
			case "incomplete-tombstone":
				state.Terminals["batch"] = updater.TerminalReceipt{Binding: protocol.UpgradeBinding{UpgradeID: "batch", InstanceID: "host"}, Phase: "cancelled"}
			case "unrelated-batch":
				r.control.UpgradeID = "other"
				r.control.CancelledUpgradeID = ""
			case "wrong-current-process":
				state.Process.LaunchID = "foreign"
				state.CommittedProof.Process = state.Process
			}
			socket := filepath.Join(t.TempDir(), "updater.sock")
			ln, err := net.Listen("unix", socket)
			if err != nil {
				t.Skip(err)
			}
			local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				stateMu.Lock()
				defer stateMu.Unlock()
				if kind == "staged-cas" && q.Method == http.MethodPost && q.URL.Path == "/cancel" {
					var req updater.ControlRequest
					if err := json.NewDecoder(q.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req.ID != "batch" || req.ExpectedPhase != "staged" {
						t.Error("unsafe cancellation", req)
					}
					cancelCalls++
					state.Terminals["batch"] = updater.TerminalReceipt{Binding: binding, Phase: "cancelled"}
					state.Operation.Phase = "cancelled"
					_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
					return
				}
				if q.Method != http.MethodGet || q.URL.Path != "/status" {
					t.Error("unexpected updater mutation", q.Method, q.URL.Path)
					w.WriteHeader(405)
					return
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			go local.Serve(ln)
			defer local.Close()
			r.cfg.UpdaterControlSocket = socket
			var seen protocol.HeartbeatPayload
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				_ = json.Unmarshal(env.Payload, &seen)
				body, _ := json.Marshal(protocol.HeartbeatResult{})
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: body}})
			}))
			defer remote.Close()
			r.c.base = remote.URL
			r.c.http = remote.Client()
			if kind == "current-batch" {
				if err := r.submitUpdater(ctx, "batch", "/unused", binding.ManifestSHA256); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "staged-cas" {
				r.maybeCancelStaged(ctx)
				r.wg.Wait()
			}
			r.heartbeat(ctx)
			if seen.Runtime == nil {
				t.Fatal("no heartbeat")
			}
			receipt := seen.Runtime.UpdaterReceipt
			if kind == "staged-cas" || kind == "current-batch" || kind == "cancelled-id" {
				if receipt == nil || receipt.Phase != "cancelled" || receipt.Binding != binding || receipt.CommittedConfirmed || receipt.RollbackConfirmed || receipt.CurrentProcessInstanceID != current.LaunchID || receipt.CurrentSHA256 != current.SHA256 {
					t.Fatalf("missing truthful no-start fact: %+v", receipt)
				}
			} else if kind == "active-commit-with-old-cancel" {
				if receipt == nil || receipt.Binding != oldBinding || receipt.Phase != "committed" || !receipt.CommittedConfirmed || receipt.RollbackConfirmed {
					t.Fatalf("old cancellation marker hid active commit: %+v", receipt)
				}
			} else if receipt != nil && receipt.Phase == "cancelled" {
				t.Fatal("invalid no-start fact", receipt)
			}
			r.wg.Wait()
			stateMu.Lock()
			if kind == "staged-cas" && cancelCalls != 1 {
				t.Errorf("CAS cancellations=%d", cancelCalls)
			}
			stateMu.Unlock()
			if r.control.Mode != protocol.RunnerMaintenance {
				t.Fatal("local proof bypassed server admission fence")
			}
		})
	}
}
