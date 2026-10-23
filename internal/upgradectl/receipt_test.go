package upgradectl

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestTickCollectsRollbackOnlyForThisRunningProcess(t *testing.T) {
	for _, mismatch := range []string{"", "process", "hash", "instance", "nonterminal"} {
		t.Run(mismatch, func(t *testing.T) {
			c, f := testController(t)
			old := updater.Version{ReleaseID: "old", Artifact: release.Artifact{SHA256: c.cfg.BinarySHA256}}
			candidate := updater.Version{ReleaseID: "new", ManifestSHA256: strings.Repeat("b", 64), Artifact: release.Artifact{SHA256: strings.Repeat("c", 64)}}
			state := updater.State{InstanceID: c.cfg.Instance, Current: old, Process: updater.Identity{PID: 12, StartTicks: 2, LaunchID: c.cfg.ProcessInstance, SHA256: c.cfg.BinarySHA256}, Operation: &updater.Operation{ID: "batch", Kind: "upgrade", Phase: "rolled_back", Previous: old, Candidate: candidate, LaunchID: c.cfg.ProcessInstance, Binding: updater.Binding{UpgradeID: "batch", InstanceID: c.cfg.Instance, ProcessInstanceID: "candidate", ReleaseID: candidate.ReleaseID, ManifestSHA256: candidate.ManifestSHA256, ArtifactSHA256: candidate.Artifact.SHA256}}}
			switch mismatch {
			case "process":
				c.cfg.ProcessInstance = "other"
			case "hash":
				c.cfg.BinarySHA256 = strings.Repeat("e", 64)
			case "instance":
				c.cfg.Instance = "other"
			case "nonterminal":
				state.Operation.Phase = "rollback_starting"
			}
			ln, err := net.Listen("unix", c.cfg.UpdaterSocket)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/status" {
					t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			done := make(chan struct{})
			go func() { defer close(done); _ = srv.Serve(ln) }()
			defer func() { _ = srv.Close(); <-done }()
			if err := c.tick(context.Background()); err != nil && mismatch != "instance" {
				t.Fatal(err)
			}
			if (f.observation.UpdaterReceipt != nil) != (mismatch == "") {
				t.Fatalf("incorrect receipt for %q: %+v", mismatch, f.observation.UpdaterReceipt)
			}
		})
	}
}
