package app

import (
	"context"
	"path/filepath"
	"reflect"
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
