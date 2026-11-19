package api

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The CLI and owner API run normally. Only the runner heartbeat is supplied
// by the fixture so the projection can be inspected while its report lags.
func TestHostMaintenanceCLIRecoveryView(t *testing.T) {
	e := newEnv(t)
	e.configure()
	c := newCapabilityCLI(t, e)
	const host = "host-a"
	runtime := protocol.RunnerRuntime{
		InstanceID:   "runner-fixture",
		Capabilities: []string{protocol.CapabilityRunnerMaintenance},
	}
	report := func(mode string, applied int64) {
		t.Helper()
		runtime.Sequence++
		runtime.Mode = mode
		runtime.AppliedControlRevision = applied
		err := e.store.Write(t.Context(), func(tx *store.Tx) error {
			return tx.PutHostRuntime(t.Context(), host, store.HostRuntimeRow{
				Runtime: runtime, LastHeartbeatAt: e.clock.Now().UnixMilli(), Online: true,
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	show := func(revision int64, mode, phase string) {
		t.Helper()
		var view struct {
			ControlRevision *int64                        `json:"control_revision"`
			Maintenance     *protocol.HostMaintenanceView `json:"maintenance"`
		}
		out := c.run("host", "show", host, "--json")
		if err := json.Unmarshal(out, &view); err != nil {
			t.Fatal(err)
		}
		if revision == 0 && view.ControlRevision != nil ||
			revision != 0 && (view.ControlRevision == nil || *view.ControlRevision != revision) {
			t.Fatalf("wrong control_revision: %s", out)
		}
		if mode == "" && view.Maintenance != nil ||
			mode != "" && (view.Maintenance == nil || view.Maintenance.Mode != mode || view.Maintenance.Phase != phase) {
			t.Fatalf("wrong maintenance projection: %s", out)
		}
	}

	report(protocol.RunnerNormal, 0)
	show(0, "", "")
	c.run("host", "set-maintenance", host, "--data", `{"mode":"maintenance"}`)
	show(1, protocol.RunnerMaintenance, "draining")
	report(protocol.RunnerReady, 1)
	show(1, protocol.RunnerReady, "ready")
	c.run("host", "maintenance", host, "--data", `{"mode":"normal"}`)
	show(2, protocol.RunnerMaintenance, "draining")
	report(protocol.RunnerNormal, 2)
	show(2, "", "")
}
