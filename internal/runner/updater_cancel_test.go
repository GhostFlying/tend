package runner

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

type stagedCancelStub struct {
	mu              sync.Mutex
	state           updater.State
	fail            bool
	advanceOnCancel bool
	cancels, stages int
}

func (s *stagedCancelStub) serve(w http.ResponseWriter, q *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		w.WriteHeader(503)
		return
	}
	if q.Method == http.MethodGet && q.URL.Path == "/status" {
		json.NewEncoder(w).Encode(updater.ControlResponse{State: s.state})
		return
	}
	var request updater.ControlRequest
	if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
		w.WriteHeader(400)
		return
	}
	if q.Method == http.MethodPost && q.URL.Path == "/cancel" {
		if s.advanceOnCancel {
			s.state.Operation.Phase = "stopping"
		}
		if request.ExpectedPhase != "staged" || s.state.Operation == nil || s.state.Operation.ID != request.ID || s.state.Operation.Phase != "staged" {
			w.WriteHeader(409)
			return
		}
		s.cancels++
		s.state.Operation.Phase = "cancelled"
		if s.state.Terminals == nil {
			s.state.Terminals = map[string]updater.TerminalReceipt{}
		}
		s.state.Terminals[request.ID] = updater.TerminalReceipt{Binding: s.state.Operation.Binding, Phase: "cancelled"}
	} else if q.Method == http.MethodPost && q.URL.Path == "/upgrade" {
		if s.state.Operation != nil && s.state.Operation.Phase != "cancelled" {
			w.WriteHeader(409)
			return
		}
		s.stages++
		s.state.Operation = &updater.Operation{ID: request.ID, Kind: "upgrade", Phase: "staged", Binding: protocol.UpgradeBinding{UpgradeID: request.ID, InstanceID: s.state.InstanceID}}
	} else {
		w.WriteHeader(404)
		return
	}
	json.NewEncoder(w).Encode(updater.ControlResponse{State: s.state})
}
func startCancelStub(t *testing.T, s *stagedCancelStub) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(s.serve)}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return socket
}
func cancelledFixture(id, phase string) updater.State {
	return updater.State{InstanceID: "host", Operation: &updater.Operation{ID: id, Kind: "upgrade", Phase: phase, Binding: protocol.UpgradeBinding{UpgradeID: id, InstanceID: "host"}}}
}

func TestStagedCancellationRetriesAcrossRestartAndAllowsNextBatch(t *testing.T) {
	stub := &stagedCancelStub{state: cancelledFixture("old-batch", "staged"), fail: true}
	r := controlRunner(t)
	r.cfg.UpdaterInstanceID = "host"
	r.cfg.UpdaterControlSocket = startCancelStub(t, stub)
	cancel := protocol.RunnerControl{Revision: 4, Mode: protocol.RunnerNormal, CancelledUpgradeID: "old-batch"}
	if err := r.applyControl(t.Context(), &cancel, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	view, err := r.runtimeObservation(t.Context())
	r.wg.Wait()
	if err != nil || view.ConfirmedCancelledUpgradeID != "" {
		t.Fatal("failed status falsely acknowledged cancellation", view, err)
	}
	stub.mu.Lock()
	stub.fail = false
	stub.mu.Unlock()
	restarted := New(r.cfg)
	if err := restarted.loadControl(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restarted.cancelledUpdaterID != "" {
		t.Fatal("restart trusted previous memory")
	}
	if _, err := restarted.runtimeObservation(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted.wg.Wait()
	view, err = restarted.runtimeObservation(t.Context())
	restarted.wg.Wait()
	if err != nil || view.ConfirmedCancelledUpgradeID != "old-batch" {
		t.Fatal("durable cancellation not recovered", view, err)
	}
	if err := restarted.submitUpdater(t.Context(), "next-batch", t.TempDir(), "manifest"); err != nil {
		t.Fatal("old staged operation blocked next release", err)
	}
	if _, err := restarted.runtimeObservation(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted.wg.Wait()
	again := New(r.cfg)
	if err := again.loadControl(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := again.runtimeObservation(t.Context()); err != nil {
		t.Fatal(err)
	}
	again.wg.Wait()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.cancels != 1 || stub.stages != 1 || stub.state.Operation.ID != "next-batch" || stub.state.Operation.Phase != "staged" {
		t.Fatalf("next batch affected: %+v", stub)
	}
}

func TestStagedCancellationRequiresExplicitMarkerAndMatchingSafeOperation(t *testing.T) {
	for _, kind := range []string{"attention-normal", "stopping", "authorized", "starting", "checking", "wrong-instance", "wrong-binding", "race-stopping", "wrong-operation"} {
		t.Run(kind, func(t *testing.T) {
			stub := &stagedCancelStub{state: cancelledFixture("old", "staged")}
			c := protocol.RunnerControl{Revision: 4, Mode: protocol.RunnerNormal, CancelledUpgradeID: "old"}
			switch kind {
			case "attention-normal":
				c.CancelledUpgradeID = ""
			case "wrong-instance":
				stub.state.InstanceID = "another"
			case "wrong-binding":
				stub.state.Operation.Binding.InstanceID = "another"
			case "race-stopping":
				stub.advanceOnCancel = true
			case "wrong-operation":
				stub.state.Operation.Kind = "restart"
			default:
				stub.state.Operation.Phase = kind
			}
			r := controlRunner(t)
			r.cfg.UpdaterInstanceID = "host"
			r.cfg.UpdaterControlSocket = startCancelStub(t, stub)
			if err := r.applyControl(t.Context(), &c, r.cfg.Now(), true); err != nil {
				t.Fatal(err)
			}
			r.wg.Wait()
			view, err := r.runtimeObservation(t.Context())
			r.wg.Wait()
			if err != nil || view.ConfirmedCancelledUpgradeID != "" {
				t.Fatal("unsafe cancel acknowledgement", view, err)
			}
			stub.mu.Lock()
			defer stub.mu.Unlock()
			if stub.cancels != 0 {
				t.Fatal("cancelled without safe staged proof")
			}
		})
	}
}

func TestSlowUpdaterCancellationDoesNotBlockControlApplication(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	socket := filepath.Join(t.TempDir(), "slow.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-q.Context().Done():
		}
	})}
	go server.Serve(ln)
	defer server.Close()
	defer close(release)
	r := controlRunner(t)
	r.cfg.UpdaterInstanceID = "host"
	r.cfg.UpdaterControlSocket = socket
	done := make(chan error, 1)
	go func() {
		done <- r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerNormal, CancelledUpgradeID: "old"}, r.cfg.Now(), true)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("control blocked on local updater")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancellation reconciliation did not start")
	}
	r.wg.Wait()
	if r.cancelledUpdaterID != "" {
		t.Fatal("timeout acknowledged cancellation")
	}
}

func TestCancellationConfirmsRetainedTerminalReceiptWithoutTouchingNewOperation(t *testing.T) {
	for _, phase := range []string{"cancelled", "committed", "rolled_back"} {
		t.Run(phase, func(t *testing.T) {
			state := cancelledFixture("new-batch", "staged")
			state.Terminals = map[string]updater.TerminalReceipt{"old-batch": {Phase: phase, Binding: protocol.UpgradeBinding{UpgradeID: "old-batch", InstanceID: "host"}}}
			stub := &stagedCancelStub{state: state}
			r := controlRunner(t)
			r.cfg.UpdaterInstanceID = "host"
			r.cfg.UpdaterControlSocket = startCancelStub(t, stub)
			if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 4, Mode: protocol.RunnerNormal, CancelledUpgradeID: "old-batch"}, r.cfg.Now(), true); err != nil {
				t.Fatal(err)
			}
			r.wg.Wait()
			view, err := r.runtimeObservation(t.Context())
			r.wg.Wait()
			if err != nil || view.ConfirmedCancelledUpgradeID != "old-batch" {
				t.Fatal("retained terminal not acknowledged", view, err)
			}
			stub.mu.Lock()
			defer stub.mu.Unlock()
			if stub.cancels != 0 || stub.state.Operation.ID != "new-batch" || stub.state.Operation.Phase != "staged" {
				t.Fatal("new operation changed")
			}
		})
	}
}
