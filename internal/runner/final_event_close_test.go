package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func finalWorkerEvents(thread string) []harness.WorkerEvent {
	terminal := workerTerminalEvent(thread, "original-turn", "completed")
	terminal.NativeID = "original-turn/completed"
	return []harness.WorkerEvent{
		{NativeID: "original-turn/final-message", Kind: "item.completed", Body: json.RawMessage(`{"type":"agentMessage","text":"The original report is complete."}`)},
		{NativeID: "original-turn/concurrency", Kind: "worker.concurrency", Body: json.RawMessage(`{}`)},
		terminal,
	}
}

func finalEventsResponse(env protocol.Envelope, failing bool) *http.Response {
	status := http.StatusOK
	response := rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}}
	if failing {
		status = http.StatusServiceUnavailable
		response = rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "event endpoint unavailable")}
	}
	body, _ := json.Marshal(response)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func TestFinalWorkerEventsUploadBeforeUnconfirmedClose(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("runner_shutdown=%t", shutdown), func(t *testing.T) {
			r, as, _, spaces, path := implementationReconcileFixture(t)
			firstImplementationAttempt(t, r, as, spaces, path)
			lifetime, stop := context.WithCancel(t.Context())
			defer stop()
			result := submittedImplementationResult()
			result.Result.Files = result.Result.Files[:3]
			workers := &normalResultWorkers{result: result, events: finalWorkerEvents("owned-thread")}
			r.cfg.Workers = workers
			var expected, uploaded []protocol.RunEvent
			workers.onWait = func() {
				var err error
				expected, err = r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10)
				if err != nil || len(expected) != 3 {
					t.Fatal("final native events were not durably queued", expected, err)
				}
				if shutdown {
					stop()
				}
			}
			requests := 0
			r.c.base = "http://runner.invalid"
			r.c.http = &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					return nil, err
				}
				deadline, bounded := req.Context().Deadline()
				if req.Context().Err() != nil || !bounded || time.Until(deadline) > stopFlush {
					t.Error("final fact upload inherited cancellation or lost its bound", req.Context().Err(), bounded)
				}
				if env.Operation != protocol.OpRunnerEvents || env.Target.ID != as.RunID || env.RequestID == "" || req.Header.Get(headerRun) != as.RunID {
					t.Error("final facts changed identity or submitted an artifact before close", env)
				}
				var payload protocol.EventsPayload
				if err := json.Unmarshal(env.Payload, &payload); err != nil {
					return nil, err
				}
				uploaded = append(uploaded, payload.Events...)
				requests++
				return finalEventsResponse(env, false), nil
			})}
			workers.onStop = func() error {
				pending, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10)
				if err != nil || len(pending) != 0 || requests != 1 {
					t.Error("physical close preceded the independent final event upload", pending, requests, err)
				}
				return fmt.Errorf("%w: original process close remains unknown", harness.ErrOwnedWorkerUnconfirmed)
			}
			err := r.work(lifetime, lifetime, as, &active{as: as, desired: protocol.DesiredRun})
			if !isCompletionPending(err) || !reflect.DeepEqual(uploaded, expected) || workers.starts != 1 {
				t.Fatal("close uncertainty hid or replayed final native facts", uploaded, expected, workers.starts, err)
			}
			saved, err := r.cfg.Journal.Run(t.Context(), as.RunID)
			if err != nil || saved.StoppedConfirmed || saved.Attempts != 1 || saved.Thread != "owned-thread" {
				t.Fatal("event acknowledgement fabricated closure or another attempt", saved, err)
			}
			if !shutdown && (saved.Phase != journal.PhaseWorkerDone || len(saved.WorkerResult) == 0) {
				t.Fatal("event upload discarded the original successful result", saved)
			}
		})
	}
}

func TestWorkerDoneFinalEventsRetryAndAckWithoutNativeReplay(t *testing.T) {
	r, as, _, _, _ := implementationReconcileFixture(t)
	body, _ := json.Marshal(submittedImplementationResult())
	if err := r.cfg.Journal.WorkerDone(t.Context(), as.RunID, body); err != nil {
		t.Fatal(err)
	}
	for _, ev := range finalWorkerEvents("original-result-thread") {
		if err := r.cfg.Journal.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{NativeID: ev.NativeID, Kind: ev.Kind, Body: ev.Body}); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10)
	if err != nil || len(expected) != 3 {
		t.Fatal(expected, err)
	}
	workers := &normalResultWorkers{onStop: func() error {
		return fmt.Errorf("%w: original process close remains unknown", harness.ErrOwnedWorkerUnconfirmed)
	}}
	r.cfg.Workers = workers
	failing := true
	var uploads [][]protocol.RunEvent
	r.c.base = "http://runner.invalid"
	r.c.http = &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			return nil, err
		}
		if env.Operation != protocol.OpRunnerEvents || env.Target.ID != as.RunID || env.RequestID == "" || req.Header.Get(headerRun) != as.RunID {
			t.Error("recovery changed fact identity or submitted an unclosed artifact", env)
		}
		var payload protocol.EventsPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return nil, err
		}
		uploads = append(uploads, payload.Events)
		return finalEventsResponse(env, failing), nil
	})}
	for pass := range 3 {
		// This is the original Continue authorization used by Hello; the saved
		// result re-enters completion without acquiring a new native attempt.
		if err := r.authorizeRetainedSettlement(t.Context(), as.RunID, true); err != nil {
			t.Fatal(err)
		}
		if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); !isOwnedRecoveryUncertain(err) {
			t.Fatal("unconfirmed close lost its bounded retained outcome", err)
		}
		pending, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10)
		if err != nil || pass == 0 && !reflect.DeepEqual(pending, expected) || pass > 0 && len(pending) != 0 {
			t.Fatal("failed upload lost facts or acknowledged facts were replayed", pass, pending, err)
		}
		failing = false
	}
	if len(uploads) != 2 || !reflect.DeepEqual(uploads[0], expected) || !reflect.DeepEqual(uploads[1], expected) || workers.starts != 0 {
		t.Fatal("completion recovery repeated native work or replaced event identities", uploads, expected, workers.starts)
	}
	saved, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil || saved.Phase != journal.PhaseWorkerDone || saved.StoppedConfirmed || !saved.Fenced || saved.Attempts != 1 || !bytes.Equal(saved.WorkerResult, body) {
		t.Fatal("event recovery changed physical closure or the original result", saved, err)
	}
}
