package runner

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestBoundedRestartAdmissionUsesActualNewProcessAndGeneration(t *testing.T) {
	if os.Getenv("TEND_TEST_BOUNDED_RESTART_CHILD") != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBoundedRestartAdmissionUsesActualNewProcessAndGeneration$", "-test.v")
		command.Env = append(os.Environ(), "TEND_TEST_BOUNDED_RESTART_CHILD=1", "TEND_UPDATER_LAUNCH_ID=fresh-monitored-launch")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("actual monitored process admission failed: %v\n%s", err, output)
		}
		return
	}
	actual, err := (updater.LinuxProcess{}).Inspect(t.Context(), os.Getpid())
	if err != nil || actual.LaunchID != "fresh-monitored-launch" {
		t.Fatal(actual, err)
	}
	for _, scenario := range []string{"valid", "missing-receipt", "wrong-binding", "wrong-process", "false-commit", "wrong-fixed-updater", "wrong-generation", "wrong-kernel-identity"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, _ := retainedStrictFixture(t)
			r.cfg.HostID, r.cfg.InstanceID, r.cfg.UpdaterInstanceID = "original-host", actual.LaunchID, "fixed-updater"
			r.cfg.Build.BinarySHA256 = actual.SHA256
			r.cfg.InitialMaintenance, r.bootstrapMaintenance = true, true
			// A monitored restart has a fresh kernel launch and no repeated
			// upgrade hint. Its durable predecessor quarantine supplies lineage.
			version := updater.Version{ReleaseID: "candidate", ManifestSHA256: strings.Repeat("b", 64), Artifact: release.Artifact{SHA256: actual.SHA256}}
			target := protocol.UpgradeBinding{UpgradeID: "original-upgrade", InstanceID: r.cfg.UpdaterInstanceID, ProcessInstanceID: "first-forced-launch", ReleaseID: version.ReleaseID, ManifestSHA256: version.ManifestSHA256, ArtifactSHA256: actual.SHA256}
			authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner", Target: target}
			bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
			if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
				t.Fatal(err)
			}
			if err := r.loadBoundedRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := r.loadControl(t.Context()); err != nil {
				t.Fatal(err)
			}
			before, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			state := updater.State{InstanceID: r.cfg.UpdaterInstanceID, Current: version, Process: actual,
				CommittedProof: &updater.CommittedProof{Binding: target, Version: version, Process: actual}}
			switch scenario {
			case "missing-receipt":
				state.CommittedProof = nil
			case "wrong-binding":
				state.CommittedProof.Binding.ProcessInstanceID = "unrelated-forced-launch"
			case "wrong-process":
				state.Process.LaunchID = "different-current-launch"
				state.CommittedProof.Process = state.Process
			case "false-commit":
				state.CommittedProof.Version.ReleaseID = "uncommitted-version"
			case "wrong-fixed-updater":
				state.InstanceID = "another-updater"
				state.CommittedProof.Binding.InstanceID = state.InstanceID
			case "wrong-kernel-identity":
				state.Process.StartTicks++
				state.CommittedProof.Process = state.Process
			}
			socket := filepath.Join(t.TempDir(), "fixed.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method != http.MethodGet || q.URL.Path != "/status" {
					t.Error("admission attempted a supervisor mutation")
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			go func() { _ = local.Serve(listener) }()
			defer local.Close()
			r.cfg.UpdaterControlSocket = socket
			data := *authority.DataIdentity
			ownerRelease := protocol.BoundedRunnerAdmissionRelease{RequestID: "fresh-owner-release", RecoveryAuthorizationID: authority.RequestID, HostID: r.cfg.HostID,
				Successor: target, RunnerGeneration: r.generation, Journal: data, ExpectedControlRevision: r.control.Revision, Reason: "admit new work while original unknown work remains isolated"}
			if scenario == "wrong-generation" {
				ownerRelease.RunnerGeneration--
			}
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				var hello protocol.HelloPayload
				_ = json.Unmarshal(env.Payload, &hello)
				if env.Operation != protocol.OpRunnerHello || hello.Runtime == nil || hello.Runtime.InstanceID != actual.LaunchID || hello.Runtime.Generation != r.generation {
					t.Error("hello reused predecessor runtime identity")
				}
				body, _ := json.Marshal(protocol.HelloResult{Control: &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerNormal, BoundedRecoveryRelease: &ownerRelease}, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}}})
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: body}})
			}))
			defer remote.Close()
			r.c.base, r.c.http = remote.URL, remote.Client()
			err = r.hello(t.Context())
			if scenario == "valid" {
				if err != nil || r.boundedRecoveryPending() || r.maintenance() {
					t.Fatal("verified monitored restart could not accept fresh owner release", err)
				}
			} else if err == nil || !r.boundedRecoveryPending() || !r.maintenance() {
				t.Fatal("unverified monitored restart accepted owner release", err)
			}
			after, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pending, requests) || !r.boundedRunQuarantined(t.Context(), as.RunID) || r.lookup(as.RunID) != nil {
				t.Fatal("monitored restart or global release changed original unknown work", after, requests)
			}
			if scenario == "valid" {
				// An exact old release can remain in host control after a
				// different force target. It is historical fact, not authority
				// over this new process, candidate or recovery window.
				next := authority
				next.RequestID = "different-force"
				next.Target.UpgradeID, next.Target.ProcessInstanceID = "different-upgrade", "different-launch"
				next.Target.ArtifactSHA256 = strings.Repeat("c", 64)
				r.cfg.UpgradeID, r.cfg.Build.BinarySHA256 = next.Target.UpgradeID, next.Target.ArtifactSHA256
				if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), next); err != nil {
					t.Fatal(err)
				}
				if err := r.loadBoundedRecovery(t.Context()); err != nil {
					t.Fatal(err)
				}
				oldControl := protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerMaintenance, BoundedRecoveryRelease: &ownerRelease}
				if err := r.applyControl(t.Context(), &oldControl, r.cfg.Now(), true); err != nil {
					t.Fatal("exact historical release blocked a different recovery target", err)
				}
				if !r.boundedRecoveryPending() || !r.boundedRunQuarantined(t.Context(), as.RunID) {
					t.Fatal("historical release authorized a different recovery window")
				}
			}
		})
	}
}
