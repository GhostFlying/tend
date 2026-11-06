package cli

import (
	"encoding/json"
	"net/http"
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
