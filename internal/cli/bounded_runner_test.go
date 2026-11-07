package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBoundedRunnerOwnerCLIUsesOriginalRequestAndHostCAS(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	plan := protocol.RunnerBoundedMaintenancePlan{
		HostID: "runner-one", NodeSelector: "node-one", RunnerInstanceID: "runner-original", RunnerGeneration: 1, ReleaseDir: filepath.Join(t.TempDir(), "reviewed-release"), ExpectedControlRevision: 4,
		Source:  protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "original-launch", SHA256: strings.Repeat("a", 64)},
		Journal: protocol.BoundedDataIdentity{Device: 1, Inode: 2, Incarnation: "original-journal", Version: 15},
		Target:  protocol.UpgradeBinding{UpgradeID: "bounded-window", InstanceID: "runner-one", ProcessInstanceID: "successor-runner", ReleaseID: "new-release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)},
	}
	plan.Authorization = protocol.BoundedMaintenanceAuthorization{
		RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", ExpectedGeneration: 0, ExpectedPhase: "prepared", PlanSHA256: strings.Repeat("d", 64),
		Role: "runner", Source: plan.Source, Target: plan.Target, DataIdentity: &plan.Journal,
		Reason: "Retain original unknown native work", ExpiresAtMillis: 9999999999999,
		Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(),
	}
	plan.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(plan)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Error(err)
		}
		if env.Operation != "hosts.authorize_bounded_maintenance" || env.Target != (protocol.Target{Type: "host", ID: plan.HostID}) || env.RequestID != plan.Authorization.RequestID || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 4 {
			t.Error("owner authorization changed its original host/request/CAS", env)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpUnknown}})
	})
	if code := e.main("host", "authorize-bounded-maintenance", plan.HostID, "--data", string(raw), "--json"); code != exitError {
		t.Fatal("unknown authorization was not preserved", code, e.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 || len(f.seen("GET /api/v1/snapshot")) != 0 {
		t.Fatal("owner authorization was not sent exactly once with supplied CAS", f.requests)
	}
	if code := e.main("host", "authorize-bounded-maintenance", plan.HostID, "--data", string(raw), "--request-id", plan.Authorization.RequestID, "--revision", "4", "--json"); code != exitError {
		t.Fatal("identical unknown retry was not preserved", code, e.errOut.String())
	}
	requests := f.seen("POST /api/v1/ops")
	if len(requests) != 2 || string(requests[0].body) != string(requests[1].body) || len(f.seen("GET /api/v1/snapshot")) != 0 {
		t.Fatal("explicit retry changed the original owner Envelope or refreshed CAS", requests)
	}
	for _, conflict := range [][]string{{"--request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEZ"}, {"--revision", "5"}, {"--revision", "-1"}} {
		args := []string{"host", "authorize-bounded-maintenance", plan.HostID, "--data", string(raw)}
		if code := e.main(append(args, conflict...)...); code != exitUsage || len(f.seen("POST /api/v1/ops")) != 2 {
			t.Fatal("conflicting owner authorization reached the server", conflict, code)
		}
	}
}

func TestBoundedRunnerOwnedExitCLIPreservesFrozenPlan(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	var plan protocol.RunnerBoundedMaintenancePlan
	if err := json.Unmarshal([]byte(boundedRunnerPlanExample()), &plan); err != nil {
		t.Fatal(err)
	}
	original := plan.Target
	plan.Source = protocol.RecoveryIdentity{}
	plan.SourceOperation = &protocol.MaintenanceOperationSource{
		OperationID: original.UpgradeID, Kind: "upgrade", Phase: "launch_exited",
		OperationSHA256: strings.Repeat("d", 64), LaunchID: original.ProcessInstanceID,
		Protocol: 2, StorageVersion: 18,
		OriginalRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEX",
		LaunchExit: &protocol.MaintenanceLaunchExitProof{
			PID: 4321, LaunchID: original.ProcessInstanceID,
			ArtifactSHA256: original.ArtifactSHA256, ExitCode: 17,
		},
	}
	plan.Target.UpgradeID = "replacement-window"
	plan.Target.ProcessInstanceID = "replacement-launch"
	plan.Authorization.Source, plan.Authorization.Target = plan.Source, plan.Target
	var err error
	plan.Authorization.PlanSHA256, err = protocol.RunnerBoundedMaintenancePlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "owned-exit-plan.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if code := e.main("host", "authorize-bounded-maintenance", plan.HostID, "--file", file, "--dry-run", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var frozen protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &frozen); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, frozen.Payload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compact.Bytes(), raw) || frozen.RequestID != plan.Authorization.RequestID || frozen.ExpectedControlRevision == nil || *frozen.ExpectedControlRevision != plan.ExpectedControlRevision || len(f.requests) != 0 {
		t.Fatal("dry run changed the frozen source operation or contacted the server", frozen, f.requests)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: frozen.RequestID, Status: protocol.OpUnknown}})
	})
	for _, input := range [][]string{{"--file", file}, {"--data", string(raw)}} {
		args := []string{"host", "authorize-bounded-maintenance", plan.HostID}
		if code := e.main(append(args, input...)...); code != exitError {
			t.Fatal("unknown owned-exit authorization was not preserved", code, e.errOut.String())
		}
	}
	requests := f.seen("POST /api/v1/ops")
	if len(requests) != 2 || string(requests[0].body) != string(requests[1].body) || len(f.seen("GET /api/v1/snapshot")) != 0 {
		t.Fatal("owned-exit retry replaced the original proof, request or host CAS", f.requests)
	}
	plan.SourceOperation.LaunchExit.ExitCode++
	changed, _ := json.Marshal(plan)
	if code := e.main("host", "authorize-bounded-maintenance", plan.HostID, "--data", string(changed)); code != exitUsage || len(f.seen("POST /api/v1/ops")) != 2 {
		t.Fatal("changed exit proof bypassed the frozen plan digest", code, f.requests)
	}
}
