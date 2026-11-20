package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRetiredEventTimeoutDoesNotStarveUsage(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	r.cfg.JournalID, r.cfg.JournalRoot = "accepting", t.TempDir()
	r.cfg.RequestTimeout = 300 * time.Millisecond
	old, err := journal.Open(t.Context(), filepath.Join(r.cfg.JournalRoot, "journal.db"), r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{RunID: "historical", EventSync: true}
	if err := old.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := old.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{Kind: "output", Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	env := protocol.Envelope{RequestID: "old-usage", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}, Payload: json.RawMessage(`{"run_id":"historical"}`)}
	if err := old.PutRequest(t.Context(), journal.Request{Envelope: env}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	var events, usage atomic.Int64
	r.c.base = "http://test"
	r.c.http = &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			return nil, err
		}
		if env.Operation == protocol.OpRunnerSyncEvents {
			events.Add(1)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		if env.Operation == protocol.OpRunnerUsageUpload {
			usage.Add(1)
		}
		return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})}
	r.nativeUsageSupported = true
	var cursor retiredCursor
	for range 2 {
		cursor, _ = r.serviceRetiredJournal(t.Context(), "", cursor, protocol.RetiredJournalWork{})
	}
	if events.Load() != 2 {
		t.Fatalf("event failure was not reached: %d", events.Load())
	}
	if usage.Load() == 0 {
		t.Fatal("independent usage upload was starved on both retired-journal passes by the event deadline")
	}
}

func TestManagerCloseSharesTheOriginalEventUpload(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	as := protocol.Assignment{RunID: "manager", Role: protocol.RoleIssueManager, EventSync: true}
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	env := protocol.Envelope{RequestID: "event", Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 2<<20)})
	if err := r.cfg.Journal.SaveManagerEvent(t.Context(), as.RunID, &env, body); err != nil {
		t.Fatal(err)
	}
	bs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	transmit := make(chan struct{})
	var starts atomic.Int64
	r.c.base = "http://test"
	r.c.http = &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			return nil, err
		}
		var part protocol.RunEventPart
		if err := json.Unmarshal(env.Payload, &part); err != nil {
			return nil, err
		}
		if part.Offset == 0 {
			if starts.Add(1) == 1 {
				close(first)
			}
			select {
			case <-transmit:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		key := fmt.Sprintf("%s/%s/%s/%d", as.RunID, part.Operation, part.SHA256, part.Size)
		next, raw, err := bs.ReceivePart(req.Context(), key, part.SHA256, part.Size, part.Offset, part.Data)
		if err != nil {
			return nil, err
		}
		stored := raw != nil
		if stored {
			if err := bs.ForgetTransfer(key); err != nil {
				return nil, err
			}
		}

		result, _ := json.Marshal(protocol.RunEventPartReceipt{SHA256: part.SHA256, Next: next, Stored: stored})
		return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}})
	})}
	sent := make(chan error, 1)
	go func() { sent <- r.flushManagerEvents(t.Context(), as) }()
	<-first
	waiting, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = r.flushManagerEvents(waiting, as)
	close(transmit)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancelled close did not release its wait", err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := r.flushManagerEvents(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatal("duplicate multipart transfer", starts.Load())
	}
}
