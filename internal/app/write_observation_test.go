package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestExecuteWriteObservationPreservesReplay(t *testing.T) {
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "tend.db")
	initial, err := openAppFixture(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	var tick int64
	var observations []store.WriteObservation
	s, err := store.OpenWithOptions(t.Context(), path, clock.Now, store.WithWriteObserver(func() time.Time {
		tick++
		return time.Unix(0, tick*int64(time.Millisecond))
	}, time.Millisecond, func(o store.WriteObservation) { observations = append(observations, o) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	observations = nil
	ids := testkit.NewIDGen(clock, 42)
	h := &harness{t: t, app: New(s, ids, clock.Now), store: s, clock: clock, ids: ids}
	env := h.env("config.sync", protocol.Target{Type: "config"}, testConfig(), nil)
	env.TraceID = ids.New(protocol.PrefixTrace)
	first := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, SyncConfig{})
	replay := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, SyncConfig{})
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay changed operation: first=%+v replay=%+v", first, replay)
	}
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(observations))
	}
	for _, o := range observations {
		if o.Source.Operation != env.Operation || o.Source.RequestID != env.RequestID || o.Source.TraceID != env.TraceID || o.Outcome != "ok" {
			t.Fatalf("write source/outcome = %+v", o)
		}
	}
	phases := map[store.WritePhase]time.Duration{}
	for _, p := range observations[0].Phases {
		phases[p.Phase] = p.Duration
	}
	for _, p := range []store.WritePhase{store.WriteReviewedContext, store.WriteApply, store.WritePersistOutcome, store.WriteAttention, store.WriteFollowup, store.WriteManagerRefresh, store.WriteMaintenance, store.WriteRequestInsert} {
		if phases[p] <= 0 {
			t.Errorf("missing phase %s: %+v", p, observations[0])
		}
	}
	if phases[store.WritePersistOutcome] <= phases[store.WriteAttention]+phases[store.WriteFollowup] || observations[0].Callback <= phases[store.WritePersistOutcome] {
		t.Errorf("nested phases must be inclusive: %+v", observations[0])
	}
	if len(observations[1].Phases) != 0 {
		t.Errorf("replay reran Apply/public tail: %+v", observations[1])
	}
	if err := s.Read(context.Background(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM request_records WHERE request_id=?", env.RequestID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("request records = %d, want 1", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerEventACKWriteObservationPreservesIdentity(t *testing.T) {
	h := newHarness(t)
	observations := make(chan store.WriteObservation, 8)
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observations <- o })(h.store)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.store.Write(t.Context(), func(*store.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(store.WithWriteSource(t.Context(), store.WriteSource{Operation: "outer.operation", RequestID: "original-request", TraceID: "original-trace"}), 50*time.Millisecond)
	defer cancel()
	if err := h.app.AcknowledgeManagerEvent(ctx, "original-run", 14, "thread", "turn", "item", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ACK error = %v", err)
	}
	o := <-observations
	if o.Source.Operation != "manager.event.ack" || o.Source.RunID != "original-run" || o.Source.EventSequence != 14 || o.Source.RequestID != "original-request" || o.Source.TraceID != "original-trace" || o.Stage != "connection_wait" || o.Callback != 0 {
		t.Fatalf("ACK timeout attribution = %+v", o)
	}
	if !strings.Contains(strings.Join(o.CallerStack, "\n"), "AcknowledgeManagerEvent") {
		t.Fatalf("wrapper hid actual ACK: %v", o.CallerStack)
	}
}

func TestClaimWriteObservationIncludesFixedStages(t *testing.T) {
	h := newHarness(t)
	h.hello("linux-1")
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1}, nil)
	var observed []store.WriteObservation
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observed = append(observed, o) })(h.store)
	// The same empty long-poll request can run again. Diagnostic additions must
	// not turn the rollback sentinel into a failed business operation.
	for range 2 {
		op := h.exec(runner("linux-1"), env, Claim{})
		if op.Status != protocol.OpSucceeded {
			t.Fatalf("empty claim changed result: %+v", op)
		}
	}
	var claims int
	for _, o := range observed {
		if o.Source.Operation != protocol.OpRunnerClaim {
			continue
		}
		claims++
		phases := map[store.WritePhase]time.Duration{}
		for _, p := range o.Phases {
			phases[p.Phase] = p.Duration
		}
		for _, phase := range []store.WritePhase{store.WriteClaimRefresh, store.WriteClaimCandidates, store.WriteClaimSelection} {
			if phases[phase] <= 0 {
				t.Fatalf("missing fixed claim stage %s: %+v", phase, o)
			}
			if phases[phase] > phases[store.WriteApply] {
				t.Fatalf("claim child exceeds inclusive apply: %+v", o)
			}
		}
		if o.Source.RequestID != env.RequestID {
			t.Fatalf("poll request identity changed: %+v", o)
		}
	}
	if claims != 2 {
		t.Fatalf("got %d claim observations, want both real applications", claims)
	}
}
