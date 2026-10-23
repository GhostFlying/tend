package runner

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestHeartbeatCarriesOnlyMatchingTerminalSupervisorRollback(t *testing.T) {
	old := updater.Version{ReleaseID: "old", Artifact: release.Artifact{SHA256: strings.Repeat("a", 64)}}
	candidate := updater.Version{ReleaseID: "new", ManifestSHA256: strings.Repeat("b", 64), Artifact: release.Artifact{SHA256: strings.Repeat("c", 64)}}
	binding := protocol.UpgradeBinding{UpgradeID: "batch", InstanceID: "host", ProcessInstanceID: "failed-candidate", ReleaseID: "new", ManifestSHA256: candidate.ManifestSHA256, ArtifactSHA256: candidate.Artifact.SHA256}
	for _, kind := range []string{"valid", "wrong-process", "wrong-hash", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.Build.BinarySHA256 = old.Artifact.SHA256
			r.cfg.UpdaterInstanceID = "host"
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
			r.cfg.Log = slog.Default()
			r.c.log = r.cfg.Log
			state := updater.State{InstanceID: "host", Current: old, Process: updater.Identity{PID: 12, StartTicks: 2, LaunchID: "instance", SHA256: old.Artifact.SHA256}, Operation: &updater.Operation{ID: "batch", Kind: "upgrade", Phase: "rolled_back", LaunchID: "instance", Previous: old, Candidate: candidate, Binding: binding}}
			switch kind {
			case "wrong-process":
				state.Process.LaunchID = "other"
				state.Operation.LaunchID = "other"
			case "wrong-hash":
				r.cfg.Build.BinarySHA256 = strings.Repeat("d", 64)
			case "incomplete":
				state.Operation.Phase = "rollback_starting"
			}
			socket := filepath.Join(t.TempDir(), "updater.sock")
			ln, err := net.Listen("unix", socket)
			if err != nil {
				t.Skipf("unix socket unavailable: %v", err)
			}
			local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != http.MethodGet || q.URL.Path != "/status" {
					t.Error("unexpected supervisor mutation")
					w.WriteHeader(405)
					return
				}
				json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			go local.Serve(ln)
			defer local.Close()
			r.cfg.UpdaterControlSocket = socket
			var seen protocol.HeartbeatPayload
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				json.NewDecoder(q.Body).Decode(&env)
				json.Unmarshal(env.Payload, &seen)
				body, _ := json.Marshal(protocol.HeartbeatResult{})
				json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: body}})
			}))
			defer remote.Close()
			r.c.base = remote.URL
			r.c.http = remote.Client()
			r.heartbeat(t.Context())
			if seen.Runtime == nil {
				t.Fatal("heartbeat runtime missing")
			}
			receipt := seen.Runtime.UpdaterReceipt
			if kind == "valid" {
				if receipt == nil || !receipt.RollbackConfirmed || receipt.Binding != binding || receipt.CurrentProcessInstanceID != "instance" {
					t.Fatal("missing validated heartbeat receipt", receipt)
				}
			} else if receipt != nil {
				t.Fatal("invalid receipt forwarded", receipt)
			}
		})
	}
}
