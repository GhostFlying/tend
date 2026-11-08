package runner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestLocalOnlySourcePromotesAfterNegotiationWithoutRunCredentialOrSlot(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 31)
	r.cfg.HostID, r.c.hostID, r.c.token = "host", "host", "host-token"
	as := protocol.Assignment{RunID: "manager", Role: protocol.RoleIssueManager, LeaseID: "old-lease", RunToken: "retired-token"}
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, "claim"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\",\"session_id\":\"thread\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.registerUsage(t.Context(), as, nativeusage.Source{Path: path, Provider: "codex", ThreadID: "thread"}); err != nil {
		t.Fatal(err)
	}
	if pending, err := r.cfg.Journal.Requests(t.Context()); err != nil || len(pending) != 0 {
		t.Fatal("old server received an unsupported operation", pending, err)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Done(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(headerRun) != "" || req.Header.Get(headerHost) != "host" || req.Header.Get("Authorization") != "Bearer host-token" {
			t.Error("history depended on its retired run credential")
		}
		var env protocol.Envelope
		_ = json.NewDecoder(req.Body).Decode(&env)
		operations = append(operations, env.Operation)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.nativeUsageSupported = true
	for range 2 {
		if err := r.syncNativeUsage(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(operations) != 2 || operations[0] != protocol.OpRunnerUsageRegister || operations[1] != protocol.OpRunnerUsageUpload {
		t.Fatal(operations)
	}
	if pending, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10); err != nil || len(pending) != 0 {
		t.Fatal("source entered manager's native event sequence", pending, err)
	}
}

func TestNativeUsageNegotiationAndUncertainReceiptKeepDurableUpload(t *testing.T) {
	r := controlRunner(t)
	env := protocol.Envelope{RequestID: "usage-request", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}, Payload: json.RawMessage(`{"run_id":"historical"}`)}
	if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{Envelope: env}); err != nil {
		t.Fatal(err)
	}
	stage := 0
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var input protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		calls = append(calls, input.RequestID)
		op := protocol.Operation{RequestID: input.RequestID, Status: protocol.OpAccepted}
		if stage == 1 {
			op.Status, op.RequestID = protocol.OpSucceeded, "other-request"
		}
		if stage == 2 {
			op.Status = protocol.OpSucceeded
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &op})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.syncNativeUsage(t.Context()); err != nil || len(calls) != 0 {
		t.Fatal("unnegotiated server received native usage", calls, err)
	}
	r.nativeUsageSupported = true
	for stage = 0; stage < 3; stage++ {
		err := r.syncNativeUsage(t.Context())
		pending, readErr := r.cfg.Journal.Requests(t.Context())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if stage < 2 && (err == nil || len(pending) != 1) {
			t.Fatal("uncertain receipt discarded the durable upload", stage, pending, err)
		}
		if stage == 2 && (err != nil || len(pending) != 0) {
			t.Fatal("committed receipt was not acknowledged", pending, err)
		}
	}
	if len(calls) != 3 || calls[0] != env.RequestID || calls[1] != env.RequestID || calls[2] != env.RequestID {
		t.Fatal("retry changed request identity", calls)
	}
}

func TestSettledUsageHistoryIsNotReadOrRegisteredAgain(t *testing.T) {
	r := controlRunner(t)
	r.nativeUsageSupported = true
	for i := range 170 {
		s := journal.UsageSource{ID: fmt.Sprint(i), RunID: "past", WriterStopped: true, RegistrationQueued: true, Coverage: "caught_up", Source: nativeusage.Source{Provider: "codex", ThreadID: "thread", Path: filepath.Join(t.TempDir(), "missing-history.jsonl")}}
		if err := r.cfg.Journal.RegisterUsageSource(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if sources, err := r.cfg.Journal.PendingUsageSources(t.Context()); err != nil || len(sources) != 0 {
		t.Fatal(len(sources), err)
	}
	if err := r.syncNativeUsage(t.Context()); err != nil {
		t.Fatal("settled history was reopened", err)
	}
	if work, err := r.cfg.Journal.HasUsageWork(t.Context()); err != nil || work {
		t.Fatal("settled history retained timer", work, err)
	}
}

func TestPendingUploadOwnsWakeInsteadOfExpiredSourceRetry(t *testing.T) {
	r := controlRunner(t)
	r.nativeUsageSupported = true
	now := time.Unix(100, 0)
	r.cfg.Now = func() time.Time { return now }
	src := journal.UsageSource{ID: "source", RunID: "past", RegistrationQueued: true, Coverage: "syncing", Source: nativeusage.Source{Provider: "codex", ThreadID: "thread", Path: "missing-history.jsonl"}}
	if err := r.cfg.Journal.RegisterUsageSource(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	env := protocol.Envelope{RequestID: "upload", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}, Payload: json.RawMessage(`{"run_id":"past"}`)}
	if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{Envelope: env}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpAccepted}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	retries := nativeusage.Retries{}
	retries.Failed("source/"+src.ID, now.Add(-3*time.Second).UnixMilli())
	active := false
	if err := r.syncNativeUsageWithRetries(t.Context(), retries, &active); err == nil {
		t.Fatal("pending receipt appeared committed")
	}
	if delay := retries.Delay(now.UnixMilli(), active); delay != 2*time.Second {
		t.Fatal("blocked source retained expired retry", delay, retries)
	}
}
