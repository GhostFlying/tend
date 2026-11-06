package app

import (
	"context"
	"path/filepath"
	"reflect"
	"sync/atomic"
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

func TestClaimAndConcurrencyWritePhases(t *testing.T) {
	h, observed := newConcurrencyObservedHarness(t)
	h.hello("linux-1")
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 2})
	h.createTask(owner, createTaskPayload{Title: "work", Spec: testSpec})
	*observed = nil
	_, ok, env := h.claim("linux-1")
	if !ok || len(*observed) != 1 {
		t.Fatalf("claim=%v observations=%+v", ok, *observed)
	}
	assertWritePhases(t, (*observed)[0], store.WriteManagerTasks, store.WriteClaimCandidates, store.WriteClaimExecutors, store.WriteConcurrencySync)
	*observed = nil
	if _, ok := h.claimWith("linux-1", env); !ok || len(*observed) != 1 || len((*observed)[0].Phases) != 0 {
		t.Fatalf("claim replay reran work: ok=%v observations=%+v", ok, *observed)
	}
	*observed = nil
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if len(*observed) != 1 {
		t.Fatalf("tick observations=%+v", *observed)
	}
	assertWritePhases(t, (*observed)[0], store.WriteConcurrencySync, store.WriteConcurrencyWait, store.WriteConcurrencyLoad)
	*observed = nil
	if err := h.app.cleanConcurrencyHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*observed) != 1 {
		t.Fatalf("history cleanup observations=%+v", *observed)
	}
	assertWritePhases(t, (*observed)[0], store.WriteConcurrencyGC)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP VIEW hosted_executions`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	*observed = nil
	if err := h.app.tickConcurrency(t.Context(), true); err == nil {
		t.Fatal("broken execution query succeeded")
	}
	if len(*observed) != 1 || (*observed)[0].Outcome != "error" || (*observed)[0].Commit != 0 {
		t.Fatalf("failed sync observations=%+v", *observed)
	}
	assertWritePhases(t, (*observed)[0], store.WriteConcurrencySync)
}

func newConcurrencyObservedHarness(t *testing.T) (*harness, *[]store.WriteObservation) {
	t.Helper()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "tend.db")
	initial, err := openAppFixture(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	var ticks atomic.Int64
	var observations []store.WriteObservation
	s, err := store.OpenWithOptions(t.Context(), path, clock.Now, store.WithWriteObserver(func() time.Time {
		return time.Unix(0, ticks.Add(1)*int64(time.Millisecond))
	}, 0, func(o store.WriteObservation) { observations = append(observations, o) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 42)
	h := &harness{t: t, app: New(s, ids, clock.Now), store: s, clock: clock, ids: ids}
	h.syncConfig(testConfig())
	return h, &observations
}

func assertWritePhases(t *testing.T, observation store.WriteObservation, phases ...store.WritePhase) {
	t.Helper()
	actual := map[store.WritePhase]time.Duration{}
	for _, phase := range observation.Phases {
		actual[phase.Phase] = phase.Duration
	}
	for _, phase := range phases {
		if actual[phase] <= 0 {
			t.Errorf("missing phase %s: %+v", phase, observation)
		}
	}
}
