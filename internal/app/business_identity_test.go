package app

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBusinessTaskIdentitySurvivesConcurrentTransportRetries(t *testing.T) {
	h := newHarness(t)
	id := h.ids.New(protocol.PrefixTask)
	input := createTaskPayload{Title: "one real responsibility", Spec: testSpec}
	const attempts = 4
	envelopes := make([]*protocol.Envelope, attempts)
	for i := range envelopes {
		envelopes[i] = h.env("tasks.create", taskTarget(id), input, nil)
	}
	var wg sync.WaitGroup
	errors := make(chan error, attempts)
	responses := make(chan protocol.Operation, attempts)
	for _, env := range envelopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, err := h.app.Execute(t.Context(), owner, env, CreateTask{})
			if err != nil {
				errors <- err
			}
			responses <- op
		}()
	}
	wg.Wait()
	close(errors)
	close(responses)
	for err := range errors {
		t.Error(err)
	}
	for op := range responses {
		if op.Status != protocol.OpSucceeded {
			t.Errorf("retry: %+v", op)
		}
	}
	if h.count("tasks") != 1 {
		t.Fatalf("created %d Tasks", h.count("tasks"))
	}
	changed := input
	changed.Spec.Goal = "a different responsibility"
	op, err := h.app.Execute(t.Context(), owner, h.env("tasks.create", taskTarget(id), changed, nil), CreateTask{})
	if protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("changed business definition was accepted", op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var tables int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name IN ('request_records','native_requests')`).Scan(&tables)
		if tables != 0 {
			t.Error("generic request ledger survived")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessClaimRetryUsesOriginalRunAndLease(t *testing.T) {
	h := newHarness(t)
	for _, title := range []string{"first", "second"} {
		op, _ := h.createTask(owner, createTaskPayload{Title: title, Spec: testSpec})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	original, ok, env := h.claim("linux-1")
	if !ok {
		t.Fatal("no assignment")
	}
	var expires int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), original.RunID)
		expires = lease.ExpiresAt
		return err
	}); err != nil {
		t.Fatal(err)
	}
	retry := *env
	retry.RequestID = h.ids.New(protocol.PrefixRequest)
	op := h.exec(runner("linux-1"), &retry, Claim{})
	var result claimResult
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &result) != nil || result.Assignment.RunID != original.RunID || result.Assignment.LeaseID != original.LeaseID {
		t.Fatal("transport retry created another execution", op)
	}
	if h.count("runs") != 1 {
		t.Fatal("claim retry allocated another Run")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), original.RunID)
		if lease.ExpiresAt != expires {
			t.Error("claim retry renewed the lease")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessNativeObservationNeverPersistsRPC(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "models", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var request protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &request) != nil || request.ID != id {
		t.Fatal(op)
	}
	reply := protocol.ManagerNativeReply{ID: id, Result: json.RawMessage(`[{"ID":"observed"}]`)}
	op = h.runAs(as, protocol.OpManagerNativeReply, reply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("manager_native_requests") != 0 {
		t.Fatal("read-only RPC entered the durable command queue")
	}
	result, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id)
	if err != nil || !ready || string(result.Result) != string(reply.Result) {
		t.Fatal(result, ready, err)
	}
	// Previous-version read-only replies had no Method. The same process can
	// acknowledge its bounded consumed delivery without retaining its response.
	if op := h.runAs(as, protocol.OpManagerNativeReply, reply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	if _, _, err := restarted.ManagerNativeResult(t.Context(), as.RunID, id); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("lost volatile response was claimed durable", err)
	}
}
