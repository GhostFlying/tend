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

func TestHeartbeatCommittedSuccessorProof(t *testing.T) {
	version := updater.Version{ReleaseID: "new", ManifestSHA256: strings.Repeat("b", 64), Artifact: release.Artifact{SHA256: strings.Repeat("c", 64)}}
	binding := protocol.UpgradeBinding{UpgradeID: "batch", InstanceID: "host", ProcessInstanceID: "original-candidate", ReleaseID: version.ReleaseID, ManifestSHA256: version.ManifestSHA256, ArtifactSHA256: version.Artifact.SHA256}
	for _, kind := range []string{"valid", "wrong-process", "wrong-host", "wrong-build", "wrong-version", "uncommitted"} {
		t.Run(kind, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.Build.BinarySHA256 = version.Artifact.SHA256
			r.cfg.UpdaterInstanceID = "host"
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
			r.cfg.Log = slog.Default()
			r.c.log = r.cfg.Log
			process := updater.Identity{PID: 12, StartTicks: 2, LaunchID: "instance", SHA256: version.Artifact.SHA256}
			state := updater.State{InstanceID: "host", Current: version, Process: process, CommittedProof: &updater.CommittedProof{Binding: binding, Version: version, Process: process}}
			switch kind {
			case "wrong-process":
				state.Process.LaunchID = "different-successor"
				state.CommittedProof.Process = state.Process
			case "wrong-host":
				state.InstanceID = "other"
				state.CommittedProof.Binding.InstanceID = "other"
			case "wrong-build":
				r.cfg.Build.BinarySHA256 = strings.Repeat("d", 64)
			case "wrong-version":
				state.Current.ReleaseID = "unrelated-release"
			case "uncommitted":
				state.CommittedProof = nil
			}
			socket := filepath.Join(t.TempDir(), "updater.sock")
			ln, err := net.Listen("unix", socket)
			if err != nil {
				t.Skipf("unix socket unavailable: %v", err)
			}
			local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != http.MethodGet || q.URL.Path != "/status" {
					t.Error("unexpected supervisor mutation")
					w.WriteHeader(http.StatusMethodNotAllowed)
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
				if receipt == nil || !receipt.CommittedConfirmed || receipt.RollbackConfirmed || receipt.Phase != "committed" || receipt.Binding != binding || receipt.CurrentProcessInstanceID != "instance" {
					t.Fatal("missing validated successor proof", receipt)
				}
			} else if receipt != nil {
				t.Fatal("unconfirmed successor forwarded", receipt)
			}
		})
	}
}
