package runnerapi

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIdleClaimIgnoresUsageProjectionButWakesForWork(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	claims := make(chan struct{}, 20)
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
		if o.Source.Operation == protocol.OpRunnerClaim {
			claims <- struct{}{}
		}
	})(e.store)
	env := e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{WorkspaceProtocol: 1})
	// A changed workspace report deliberately reaches the ordinary writer on
	// every attempted claim, making an unwanted wake observable despite the
	// read-side idle fast path. The no-effect claim rolls this update back.
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE hosts SET workspace_protocol=0 WHERE host_id='linux-1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := make(chan *protocol.Assignment, 1)
	go func() {
		got <- e.claim(env)
	}()
	select {
	case <-claims:
	case <-time.After(time.Second):
		t.Fatal("initial claim did not reach writer")
	}
	for range 3 {
		if err := e.app.RecordTokenUsage(t.Context(), "main", protocol.TokenUsageSample{Source: "usage", RequestID: "original", ThreadID: "native", Revision: 1, Settlement: "final"}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-claims:
		t.Fatal("projection-only update reran the full claim writer")
	case <-time.After(100 * time.Millisecond):
	}
	e.createTask()
	select {
	case assignment := <-got:
		if assignment == nil {
			t.Fatal("business scheduling change did not allocate work")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("claim did not observe new work")
	}
}
