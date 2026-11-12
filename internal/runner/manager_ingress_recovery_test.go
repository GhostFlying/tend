package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestManagerRejectedOriginalRecordsPhysicalFactAndRecoversSameTail(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	// This is the already negotiated optional close-fact capability. Its source
	// is independent from the original ordered manager event and final stopped RPC.
	as.SessionStopReports = true
	// Exceed the journal spool threshold; the separate real HTTP regression
	// owns the multi-MiB intake contract. Keep this lifecycle test inside its
	// unchanged one-second transport budget under the race detector.
	var originals []protocol.Envelope
	for _, body := range []json.RawMessage{json.RawMessage(`{"native":{"Kind":"item_completed","Tool":{"Name":"context","Output":"` + strings.Repeat("original", 5000) + `"}}}`), json.RawMessage(`{"disconnected":true}`)} {
		env, err := r.newEnvelope(protocol.OpManagerNativeEvent, runTarget(as), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.cfg.Journal.SaveManagerEvent(t.Context(), as.RunID, env, body); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, *env)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	stopped, err := r.newEnvelope(protocol.OpManagerNativeStopped, runTarget(as), protocol.ManagerStoppedPayload{Reason: "actual original process exited", LastSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Finish(t.Context(), as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *stopped}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	accept, physical := false, false
	var received []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch env.Operation {
		case protocol.OpRunsRecordSessionStop:
			physical = true
		case protocol.OpManagerNativeEvent:
			if !accept {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInvalidRequest, "original intake unavailable")})
				return
			}
			index := len(received)
			if index >= len(originals) || env.RequestID != originals[index].RequestID || !bytes.Equal(env.Payload, originals[index].Payload) {
				t.Error("original event changed or was reordered")
			}
			received = append(received, env)
		case protocol.OpManagerNativeStopped:
			if len(received) != 2 || env.RequestID != stopped.RequestID || !bytes.Equal(env.Payload, stopped.Payload) {
				t.Error("stop overtook original events")
			}
		default:
			t.Error("recovery performed unrelated work", env.Operation)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.reportManagerStopped(t.Context(), as); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal(err)
	}
	mu.Lock()
	seenPhysical := physical
	mu.Unlock()
	if !seenPhysical {
		t.Fatal("rejected body hid confirmed physical close")
	}
	pending, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(pending) != 3 {
		t.Fatal("rejection lost original pending", len(pending), err)
	}
	run, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil || !run.StoppedConfirmed || run.Phase != journal.PhaseReleasing {
		t.Fatal("physical fact declared business complete", run, err)
	}
	mu.Lock()
	accept = true
	mu.Unlock()
	if err := r.reportManagerStopped(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cfg.Journal.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("ordered recovery did not settle", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatal("tail duplicated", len(received))
	}
}

func TestManagerPermanentRejectionWaitsBeforeReconciliation(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	r.cfg.RetryDelay = 80 * time.Millisecond
	event, err := r.newEnvelope(protocol.OpManagerNativeEvent, runTarget(as), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SaveManagerEvent(t.Context(), as.RunID, event, json.RawMessage(`{"disconnected":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetPhase(t.Context(), as.RunID, journal.PhaseReleasing); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInvalidRequest, "same rejected original")})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	start := time.Now()
	err = r.executeManager(t.Context(), t.Context(), &active{as: as})
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < r.cfg.RetryDelay {
		t.Fatal("executor immediately reentered reconciliation", elapsed)
	}
	select {
	case <-r.reconcile:
	default:
		t.Fatal("eventual reconciliation lost")
	}
	pending, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Envelope.RequestID != event.RequestID || !bytes.Equal(pending[0].Envelope.Payload, event.Payload) {
		t.Fatal("permanent rejection changed pending original", err)
	}
	// Cancellation ends the wait rather than acquiring another retry budget.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start = time.Now()
	_ = r.executeManager(ctx, ctx, &active{as: as})
	if time.Since(start) >= r.cfg.RetryDelay {
		t.Fatal("shutdown waited out retry")
	}
}
