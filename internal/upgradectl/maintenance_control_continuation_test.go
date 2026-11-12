//go:build linux

package upgradectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/testkit/storefixture"
	"github.com/GhostFlying/tend/internal/updater"
)

// controlContinuationAuthority delegates every control to the real App handler.
// Its only injected fault is losing a successful transport response after commit.
type controlContinuationAuthority struct {
	*updater.HTTPAuthority
	calls     int
	loseAfter int
}

func (a *controlContinuationAuthority) MaintenanceArchiveControl(ctx context.Context, path string, input any) error {
	a.calls++
	if err := a.HTTPAuthority.MaintenanceArchiveControl(ctx, path, input); err != nil {
		return err
	}
	if a.calls == a.loseAfter {
		return errors.New("injected response lost after real control commit")
	}
	return nil
}

type controlContinuationProcess struct {
	identity  updater.Identity
	mutations int
}

func (p *controlContinuationProcess) Inspect(context.Context, int) (updater.Identity, error) {
	return p.identity, nil
}
func (p *controlContinuationProcess) Find(context.Context, string, string) ([]updater.Identity, error) {
	return []updater.Identity{p.identity}, nil
}
func (p *controlContinuationProcess) Start(context.Context, string, updater.Config, map[string]string) (updater.Identity, error) {
	p.mutations++
	return updater.Identity{}, errors.New("unexpected process start")
}
func (p *controlContinuationProcess) Stop(context.Context, updater.Identity, time.Duration) error {
	p.mutations++
	return errors.New("unexpected process stop")
}

// Only closed empty-schema bytes are shared; every case owns its DB and originals.
var maintenanceControlFixtureTemplate storefixture.Template

type controlContinuationFixture struct {
	t          *testing.T
	a          *app.App
	db         *store.Store
	ids        protocol.IDGen
	clock      *testkit.Clock
	cfg        updater.Config
	opts       updater.Options
	authority  *controlContinuationAuthority
	process    *controlContinuationProcess
	plan       protocol.CommittedMaintenancePlan
	supervisor *updater.Supervisor
}

func newControlContinuationFixture(t *testing.T) *controlContinuationFixture {
	t.Helper()
	f := &controlContinuationFixture{t: t, clock: testkit.NewClock()}
	f.ids = testkit.NewIDGen(f.clock, 992)
	var err error
	f.db, err = maintenanceControlFixtureTemplate.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	f.a = app.New(f.db, f.ids, f.clock.Now)
	f.execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Hosts: []app.HostConfig{
		{HostID: "runner-a", OS: "linux", Arch: "amd64", Slots: 1, Nodes: []string{"node-a"}, TokenHash: strings.Repeat("a", 64)},
		{HostID: "runner-b", OS: "linux", Arch: "amd64", Slots: 1, Nodes: []string{"node-b"}, TokenHash: strings.Repeat("b", 64)},
		{HostID: "kept", OS: "linux", Arch: "amd64", Slots: 1, Nodes: []string{"node-c"}, TokenHash: strings.Repeat("c", 64)},
	}}, nil, app.SyncConfig{})
	f.plan = protocol.CommittedMaintenancePlan{MaintenanceID: "control-continuation", PreparationRevision: 15, PreparationRequestID: f.id(), PreparationCancelRequestID: f.id(), RequestID: f.id(), LinkRequestID: f.id(), ExpiresAtMillis: f.clock.Now().Add(10 * time.Minute).UnixMilli()}
	for i, host := range []string{"runner-a", "runner-b", "kept"} {
		revision := int64(230 - i*10)
		mode := protocol.RunnerNormal
		if host == "kept" {
			mode = protocol.RunnerMaintenance
		}
		f.plan.HostControls = append(f.plan.HostControls, protocol.MaintenanceHostControl{HostID: host, Revision: revision, PreviousMode: mode, RequestID: f.id(), RestoreRequestID: f.id()})
		if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
			return tx.PutHostControl(t.Context(), host, protocol.RunnerControl{Revision: revision, Mode: mode}, f.clock.Now().UnixMilli())
		}); err != nil {
			t.Fatal(err)
		}
		rt := &protocol.RunnerRuntime{InstanceID: host, Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: mode, AppliedControlRevision: revision, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance}}
		f.execute(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: host}, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: host}, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: rt}, nil, app.Hello{})
	}
	if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE archive_preparation SET revision=14 WHERE singleton=1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "tend-cc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "authority.sock")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- ServeLocal(ctx, socket, ArchiveControl{App: f.a, ObserveHosted: func(context.Context) (bool, error) { return true, nil }}.Handler())
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive socket unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	f.cfg = updater.Config{InstanceID: "control-continuation", Role: "server", StateDir: filepath.Join(t.TempDir(), "state"), InitialBinary: filepath.Join(root, "server"), LogPath: filepath.Join(root, "server.log"), AuthoritySocket: socket, AuthorizeURL: "http://localhost/authorize", HealthURL: "http://localhost/health"}
	if err := os.MkdirAll(f.cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	httpAuthority, err := updater.NewHTTPAuthority(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.authority = &controlContinuationAuthority{HTTPAuthority: httpAuthority}
	f.process = &controlContinuationProcess{identity: updater.Identity{PID: 12, StartTicks: 34, LaunchID: "original-launch", SHA256: strings.Repeat("a", 64)}}
	f.plan.PreviousProcess = f.process.identity
	f.opts = updater.Options{Authority: f.authority, Process: f.process, Now: f.clock.Now, NewID: func() (string, error) { return f.id(), nil }, BootClock: func() (string, int64, error) { return "control-boot", 1000, nil }}
	state := updater.State{Format: 3, InstanceID: f.cfg.InstanceID, Generation: 1, UpdaterEpoch: "original-epoch", Maintenance: &updater.CommittedMaintenance{Plan: f.plan, Phase: "updater_adopted"}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(f.cfg.StateDir, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	t.Cleanup(func() {
		if f.supervisor != nil {
			_ = f.supervisor.Close()
		}
		if f.process.mutations != 0 {
			t.Error("control convergence changed a service process")
		}
	})
	return f
}
func (f *controlContinuationFixture) id() string { return f.ids.New(protocol.PrefixRequest) }
func (f *controlContinuationFixture) execute(p protocol.Principal, op string, target protocol.Target, input any, revision *int64, command app.Command) {
	f.t.Helper()
	raw, _ := json.Marshal(input)
	result, err := f.a.Execute(f.t.Context(), p, &protocol.Envelope{RequestID: f.id(), Operation: op, Target: target, ExpectedControlRevision: revision, Payload: raw}, command)
	if err != nil || result.Error != nil {
		f.t.Fatal(result, err)
	}
}
func (f *controlContinuationFixture) reopen() {
	f.t.Helper()
	if f.supervisor != nil {
		if err := f.supervisor.Close(); err != nil {
			f.t.Fatal(err)
		}
	}
	var err error
	f.supervisor, err = updater.Open(f.cfg, f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *controlContinuationFixture) phase(phase string) {
	f.t.Helper()
	state := f.supervisor.Status()
	if err := f.supervisor.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.supervisor = nil
	state.Maintenance.Phase = phase
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(f.cfg.StateDir, "state.json"), raw, 0600); err != nil {
		f.t.Fatal(err)
	}
	f.reopen()
}
func (f *controlContinuationFixture) prepare() error {
	return f.supervisor.PrepareCommittedMaintenance(f.t.Context(), f.plan, f.supervisor.Status().Generation)
}
func (f *controlContinuationFixture) finish() error {
	return f.supervisor.FinishCommittedMaintenance(f.t.Context(), f.plan, f.supervisor.Status().Generation)
}
func (f *controlContinuationFixture) status() protocol.ArchiveOnlineStatus {
	f.t.Helper()
	v, err := f.authority.MaintenanceArchiveStatus(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}
func (f *controlContinuationFixture) assertRestored() {
	f.t.Helper()
	v := f.status()
	if v.Preparation.Prepared || v.Preparation.Revision != 16 {
		f.t.Fatal("archive not restored", v.Preparation)
	}
	for _, h := range v.Hosts {
		for _, p := range f.plan.HostControls {
			if h.HostID == p.HostID {
				want := p.Revision
				if p.PreviousMode == protocol.RunnerNormal {
					want += 2
				}
				if h.Control.Mode != p.PreviousMode || h.Control.Revision != want {
					f.t.Fatal("host not restored", h.HostID, h.Control)
				}
			}
		}
	}
}

func TestMaintenanceControlContinuationActualAppFinish(t *testing.T) {
	f := newControlContinuationFixture(t)
	if err := f.prepare(); err != nil {
		t.Fatal(err)
	}
	f.phase("ledger_linked") // Physical installation is outside this control-only fixture.
	if err := f.finish(); err != nil {
		t.Fatal("real App rejected completion of established controls", err)
	}
	f.assertRestored()
	before := f.status()
	calls := f.authority.calls
	f.reopen()
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.status()) || f.authority.calls != calls {
		t.Fatal("repeat finish changed control SoT")
	}
}

func TestMaintenanceControlContinuationLostRepliesAndReopen(t *testing.T) {
	for _, stage := range []string{"prepare", "restore"} {
		for boundary := 1; boundary <= 3; boundary++ {
			t.Run(stage+"-"+string(rune('0'+boundary)), func(t *testing.T) {
				f := newControlContinuationFixture(t)
				if stage == "prepare" {
					f.authority.loseAfter = boundary
					if err := f.prepare(); err == nil {
						t.Fatal("lost prepare response was hidden")
					}
					f.reopen()
				}
				if err := f.prepare(); err != nil {
					t.Fatal("prepare did not converge from actual committed controls", err)
				}
				if f.authority.calls != 3 {
					t.Fatal("prepare resubmitted a satisfied control", f.authority.calls)
				}
				f.phase("ledger_linked")
				if stage == "restore" {
					f.authority.loseAfter = f.authority.calls + boundary
					if err := f.finish(); err == nil {
						t.Fatal("lost restore response was hidden")
					}
					f.reopen()
				}
				if err := f.finish(); err != nil {
					t.Fatal("restore did not converge from actual committed controls", err)
				}
				f.assertRestored()
				if f.authority.calls != 6 {
					t.Fatal("restore resubmitted a satisfied control", f.authority.calls)
				}
				before := f.status()
				f.reopen()
				if err := f.finish(); err != nil {
					t.Fatal(err)
				}
				if f.authority.calls != 6 || !reflect.DeepEqual(before, f.status()) {
					t.Fatal("repeated finish was not a no-op")
				}
			})
		}
	}
}

func TestMaintenanceControlContinuationPartialAndUntouchedCancellation(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "untouched"
		if partial {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			f := newControlContinuationFixture(t)
			before := f.status()
			if partial {
				f.authority.loseAfter = 2
				if err := f.prepare(); err == nil {
					t.Fatal("expected partial prepare response loss")
				}
				f.reopen()
			}
			if err := f.supervisor.CancelCommittedMaintenance(t.Context(), f.plan, f.supervisor.Status().Generation); err != nil {
				t.Fatal(err)
			}
			after := f.status()
			if !partial {
				if f.authority.calls != 0 || !reflect.DeepEqual(before, after) {
					t.Fatal("untouched cancellation created controls")
				}
			} else {
				if after.Preparation.Prepared || after.Preparation.Revision != 16 || f.authority.calls != 4 {
					t.Fatal("partial cancellation did not only restore existing controls", after.Preparation, f.authority.calls)
				}
				for _, h := range after.Hosts {
					for _, p := range f.plan.HostControls {
						if h.HostID == p.HostID {
							want := p.Revision
							if h.HostID == "runner-a" {
								want += 2
							}
							if h.Control.Mode != p.PreviousMode || h.Control.Revision != want {
								t.Fatal("partial cancellation touched an unprepared host", h.HostID, h.Control)
							}
						}
					}
				}
			}
			f.reopen()
			calls := f.authority.calls
			if err := f.supervisor.CancelCommittedMaintenance(t.Context(), f.plan, 0); err != nil {
				t.Fatal(err)
			}
			if calls != f.authority.calls || !reflect.DeepEqual(after, f.status()) {
				t.Fatal("cancel replay changed actual controls")
			}
		})
	}
}

func TestMaintenanceControlContinuationOperatorRestoredTargets(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		name := "all-restored"
		if cancelFirst {
			name = "archive-cancelled-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newControlContinuationFixture(t)
			if err := f.prepare(); err != nil {
				t.Fatal(err)
			}
			cancel := protocol.ArchiveControlRequest{RequestID: f.plan.PreparationCancelRequestID, ExpectedControlRevision: 15}
			if cancelFirst {
				if err := f.authority.HTTPAuthority.MaintenanceArchiveControl(t.Context(), "cancel", cancel); err != nil {
					t.Fatal(err)
				}
			}
			if !cancelFirst {
				for _, h := range f.plan.HostControls {
					if h.PreviousMode == protocol.RunnerNormal {
						if err := f.authority.HTTPAuthority.MaintenanceArchiveControl(t.Context(), "host", protocol.ArchiveHostControlRequest{RequestID: h.RestoreRequestID, ExpectedControlRevision: h.Revision + 1, HostID: h.HostID, Mode: h.PreviousMode}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := f.authority.HTTPAuthority.MaintenanceArchiveControl(t.Context(), "cancel", cancel); err != nil {
					t.Fatal(err)
				}
			}
			f.phase("ledger_linked")
			calls := f.authority.calls
			before := f.status()
			if err := f.finish(); err != nil {
				t.Fatal(err)
			}
			f.assertRestored()
			want := calls
			if cancelFirst {
				want += 2
			}
			if f.authority.calls != want {
				t.Fatal("finish repeated an operator-restored control", f.authority.calls, want)
			}
			if !cancelFirst && !reflect.DeepEqual(before, f.status()) {
				t.Fatal("operator-restored16/232/222 controls changed")
			}
			if err := f.prepare(); err == nil {
				t.Fatal("prepare reopened a completed control window")
			}
		})
	}
}

func TestMaintenanceControlContinuationRejectsForeignControlDrift(t *testing.T) {
	cases := []struct {
		name   string
		change func(*controlContinuationFixture)
	}{
		{"archive-later-revision", func(f *controlContinuationFixture) {
			if err := f.db.Write(f.t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(f.t.Context(), `UPDATE archive_preparation SET revision=revision+2`)
				return err
			}); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"host-later-same-mode", func(f *controlContinuationFixture) {
			f.execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "hosts.maintenance", protocol.Target{Type: "host", ID: "runner-a"}, protocol.HostMaintenancePayload{Mode: protocol.RunnerMaintenance}, controlRevision(231), app.HostMaintenance{})
		}},
		{"host-wrong-mode", func(f *controlContinuationFixture) {
			if err := f.db.Write(f.t.Context(), func(tx *store.Tx) error {
				return tx.PutHostControl(f.t.Context(), "runner-a", protocol.RunnerControl{Revision: 231, Mode: protocol.RunnerNormal}, f.clock.Now().UnixMilli())
			}); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"host-upgrade-control", func(f *controlContinuationFixture) {
			if err := f.db.Write(f.t.Context(), func(tx *store.Tx) error {
				return tx.PutHostControl(f.t.Context(), "runner-a", protocol.RunnerControl{Revision: 231, Mode: protocol.RunnerMaintenance, UpgradeID: "other-upgrade"}, f.clock.Now().UnixMilli())
			}); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"host-missing", func(f *controlContinuationFixture) {
			if err := f.db.Write(f.t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(f.t.Context(), `UPDATE hosts SET status='retired' WHERE host_id='runner-a'`)
				return err
			}); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newControlContinuationFixture(t)
			if err := f.prepare(); err != nil {
				t.Fatal(err)
			}
			tc.change(f)
			before := f.status()
			calls := f.authority.calls
			if err := f.prepare(); err == nil {
				t.Fatal("prepare adopted foreign control drift")
			}
			if err := f.supervisor.CancelCommittedMaintenance(t.Context(), f.plan, f.supervisor.Status().Generation); err == nil {
				t.Fatal("cancel adopted foreign control drift")
			}
			f.phase("ledger_linked")
			if err := f.finish(); err == nil {
				t.Fatal("finish adopted foreign control drift")
			}
			if f.authority.calls != calls || !reflect.DeepEqual(before, f.status()) {
				t.Fatal("foreign drift caused control writes")
			}
		})
	}
}

func controlRevision(v int64) *int64 { return &v }

func TestMaintenanceControlContinuationCancelLostRepliesAndReopen(t *testing.T) {
	for boundary := 1; boundary <= 3; boundary++ {
		t.Run(string(rune('0'+boundary)), func(t *testing.T) {
			f := newControlContinuationFixture(t)
			if err := f.prepare(); err != nil {
				t.Fatal(err)
			}
			f.authority.loseAfter = f.authority.calls + boundary
			if err := f.supervisor.CancelCommittedMaintenance(t.Context(), f.plan, f.supervisor.Status().Generation); err == nil {
				t.Fatal("lost cancel response was hidden")
			}
			if f.supervisor.Status().Maintenance.Phase != "cancelling" {
				t.Fatal("cancellation responsibility disappeared")
			}
			f.reopen()
			if err := f.supervisor.CancelCommittedMaintenance(t.Context(), f.plan, f.supervisor.Status().Generation); err != nil {
				t.Fatal(err)
			}
			f.assertRestored()
			if f.authority.calls != 6 {
				t.Fatal("cancel replayed committed controls", f.authority.calls)
			}
		})
	}
}

func TestMaintenanceControlContinuationBoundedCompletionKeepsOriginalWindow(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "available"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			f := newControlContinuationFixture(t)
			if err := f.prepare(); err != nil {
				t.Fatal(err)
			}
			f.phase("ledger_linked")
			state := f.supervisor.Status()
			if err := f.supervisor.Close(); err != nil {
				t.Fatal(err)
			}
			f.supervisor = nil
			prior := &protocol.BoundedMaintenanceWindowSnapshot{Phase: "complete", Outcome: "timed_out", PhaseDeadlineMillis: f.clock.Now().Add(-time.Minute).UnixMilli()}
			state.Bounded = &protocol.BoundedMaintenanceState{Phase: "complete", Outcome: "running", BootID: "control-boot", PhaseDeadlineMillis: f.clock.Now().Add(time.Minute).UnixMilli(), OverallDeadlineMillis: f.clock.Now().Add(2 * time.Minute).UnixMilli(), PhaseDeadlineMono: 61000, OverallDeadlineMono: 121000, Audit: []protocol.BoundedMaintenanceAudit{{RequestID: f.id(), Action: "extend", PreviousWindow: prior}}}
			if expired {
				state.Bounded.PhaseDeadlineMillis = f.clock.Now().UnixMilli() - 1
			}
			raw, _ := json.Marshal(state)
			if err := os.WriteFile(filepath.Join(f.cfg.StateDir, "state.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			f.reopen()
			before := f.supervisor.Status()
			calls := f.authority.calls
			err := f.finish()
			after := f.supervisor.Status()
			if expired {
				if err == nil || after.Bounded.Outcome != "installed_recovery_pending" || f.authority.calls != calls {
					t.Fatal("expired finish changed controls or hid pending installation", err)
				}
			} else {
				if err != nil || after.Bounded.Outcome != "completed" {
					t.Fatal("bounded completion rejected real control convergence", err)
				}
				f.assertRestored()
				f.reopen()
				calls = f.authority.calls
				if err := f.finish(); err != nil || f.authority.calls != calls {
					t.Fatal("completed bounded finish repeated controls", err)
				}
			}
			if before.Bounded.PhaseDeadlineMillis != after.Bounded.PhaseDeadlineMillis || before.Bounded.OverallDeadlineMillis != after.Bounded.OverallDeadlineMillis || !reflect.DeepEqual(before.Bounded.Audit[0], after.Bounded.Audit[0]) || !reflect.DeepEqual(before.Current, after.Current) || before.Process != after.Process {
				t.Fatal("control recovery changed original window, history or service identity")
			}
		})
	}
}
