package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_ManagerOptionalStopCannotStarveReleasingTail(t *testing.T) {
	r, as, closeEnv, stopEnv := stoppedManagerRecoveryJournal(t, true)
	as.SessionStopReports = true
	r.cfg.RequestTimeout = time.Second
	var mu sync.Mutex
	var optional []protocol.Envelope
	stops, closes := 0, 0
	acceptClose := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunsRecordSessionStop {
			mu.Lock()
			optional = append(optional, env)
			mu.Unlock()
			<-q.Context().Done()
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch env.Operation {
		case protocol.OpManagerNativeStopped:
			if !reflect.DeepEqual(env, stopEnv) {
				t.Error("physical stop identity changed", env)
			}
			stops++
		case protocol.OpManagerNativeReply:
			if !sameManagerReceiptEnvelope(env, closeEnv) || stops != 1 {
				t.Error("unknown close changed or overtook physical stop", env, stops)
			}
			closes++
			if !acceptClose {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "original business receipts remain pending")})
				return
			}
		default:
			t.Error("recovery attempted unrelated work", env.Operation)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.executeManager(t.Context(), t.Context(), &active{as: as}); err == nil {
		t.Fatal("unknown original close became successful")
	}
	mu.Lock()
	gotStops, gotCloses := stops, closes
	mu.Unlock()
	if gotStops != 1 || gotCloses == 0 {
		t.Fatal("optional metadata consumed the required recovery budget", gotStops, gotCloses)
	}
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(requests) != 1 || !sameManagerReceiptEnvelope(requests[0].Envelope, closeEnv) {
		t.Fatal("unknown lost its exact original close", requests, err)
	}
	saved, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil || saved.Phase != journal.PhaseReleasing || !saved.StoppedConfirmed {
		t.Fatal("optional failure changed actual ownership", saved, err)
	}
	// Reopen the same journal. Only the still-pending original close may repeat.
	reopenOutcomeJournal(t, r, filepath.Join(r.cfg.Journal.Directory(), "journal.db"))
	restarted := New(r.cfg)
	restarted.c.base, restarted.c.http = server.URL, server.Client()
	mu.Lock()
	acceptClose = true
	mu.Unlock()
	if err := restarted.executeManager(t.Context(), t.Context(), &active{as: as}); err != nil {
		t.Fatal("reopened mandatory tail starved behind optional metadata", err)
	}
	if _, err := restarted.cfg.Journal.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("acknowledged original tail did not release local ownership", err)
	}
	retained, err := restarted.cfg.Journal.SessionStopReport(t.Context(), as.RunID)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || len(optional) != 2 || !reflect.DeepEqual(optional[0], optional[1]) || retained == nil || !reflect.DeepEqual(*retained, optional[0]) || stops != 1 {
		t.Fatal("unknown optional envelope changed or replaced original receipts", optional, retained, stops, err)
	}
}

func TestFault_ManagerOptionalStopCannotStarveInitialNativeStop(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	as.SessionStopReports = true
	as.Execution = &protocol.RunExecutionPin{}
	as.Manager = &protocol.IssueManagerAssignment{ConversationID: "original-issue"}
	r.cfg.RequestTimeout = time.Second
	resolver := &closeRecoveryResolver{native: testkit.NewHarness()}
	r.cfg.Executions = resolver
	original, err := r.newEnvelope(protocol.OpManagerNativeEvent, runTarget(as), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SaveManagerEvent(t.Context(), as.RunID, original, json.RawMessage(`{"native":{"Kind":"item_completed","Text":"retained original"}}`)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []protocol.Envelope
	var optional *protocol.Envelope
	claims, stops := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunsRecordSessionStop {
			mu.Lock()
			saved := env
			optional = &saved
			mu.Unlock()
			<-q.Context().Done()
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch env.Operation {
		case protocol.OpManagerNativeClaim:
			claims++
			// End the one opened fake native session. No turn or tool is run.
			resolver.native.Crash()
		case protocol.OpManagerNativeEvent:
			var event protocol.ManagerNativeEvent
			if err := json.Unmarshal(env.Payload, &event); err != nil || event.Sequence != int64(len(events)+1) {
				t.Error("tail sequence changed", event, err)
			}
			if len(events) == 0 && !reflect.DeepEqual(env, *original) {
				t.Error("original pending event changed", env)
			}
			events = append(events, env)
		case protocol.OpManagerNativeStopped:
			var stop protocol.ManagerStoppedPayload
			if err := json.Unmarshal(env.Payload, &stop); err != nil || stop.LastSequence != 2 || len(events) != 2 || optional == nil {
				t.Error("stop lost the original tail or optional physical attempt", stop, len(events), err)
			}
			stops++
		default:
			t.Error("initial physical-stop recovery invoked unrelated work", env.Operation)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.executeManager(t.Context(), t.Context(), &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal("initial stopped handoff inherited exhausted optional deadline", err)
	}
	if _, err := r.cfg.Journal.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("initial stopped handoff did not settle its real tail", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if resolver.opens.Load() != 1 || len(resolver.native.Starts()) != 0 || claims != 1 || stops != 1 || len(events) != 2 {
		t.Fatal("recovery replayed native work or lost original events", resolver.opens.Load(), claims, stops, len(events))
	}
}

func TestFault_ManagerOptionalStopPreservesCallerCancellation(t *testing.T) {
	r, as, closeEnv, stopEnv := stoppedManagerRecoveryJournal(t, true)
	as.SessionStopReports = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation != protocol.OpRunsRecordSessionStop {
			t.Error("cancelled runner continued mandatory delivery", env.Operation)
		}
		cancel()
		<-q.Context().Done()
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.reportManagerStopped(ctx, as); !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation did not stop receipt processing", err)
	}
	pending, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(pending) != 2 {
		t.Fatal("cancellation discarded original pending", pending, err)
	}
	for _, p := range pending {
		if !sameManagerReceiptEnvelope(p.Envelope, closeEnv) && !reflect.DeepEqual(p.Envelope, stopEnv) {
			t.Fatal("cancellation changed original receipt", p.Envelope)
		}
	}
}
