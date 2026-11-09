package runner

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestLocalUsageJournalCarriesSnapshotCASAcrossJSON(t *testing.T) {
	r := controlRunner(t)
	r.nativeUsageSupported = true
	r.cfg.HostID = "host"
	post := func(path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r.localUsageJournal(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		return w
	}
	source := journal.UsageSource{ID: "source", RunID: "run", WriterStopped: true, Source: nativeusage.Source{Provider: "codex", ThreadID: "thread", Path: "history"}}
	w := post("/runner/v1/usage/source", source)
	var snapshot journal.UsageSourceSnapshot
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || snapshot.ExpectedRevision < 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	input := journal.UsageQueueRequest{UsageSourceSnapshot: snapshot, Envelope: &protocol.Envelope{RequestID: "original", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}, Payload: json.RawMessage(`{"run_id":"run","samples":[]}`)}}
	input.Source.Coverage = "caught_up"
	input.Source.Collection.ReadSucceeded(r.cfg.Now().UnixMilli(), false, true, true, true)
	w = post("/runner/v1/usage/queue", input)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = post("/runner/v1/usage/queue", input); w.Code != http.StatusConflict {
		t.Fatal("old snapshot accepted", w.Code, w.Body.String())
	}
	pending, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Envelope.RequestID != "original" {
		t.Fatal(pending, err)
	}
}

func TestCollectionRetirementStillReplaysOriginalUnconfirmedReport(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 81)
	r.cfg.HostID, r.c.hostID = "host", "host"
	c := protocol.NewUsageCollection(false, true)
	c.Stopped(r.cfg.Now().UnixMilli(), "closed-writer", false)
	c.RetireUnavailable(r.cfg.Now().UnixMilli(), true)
	s := journal.UsageSource{ID: "source", RunID: "run", RegistrationQueued: true, WriterStopped: true, Coverage: "pending", Collection: c, Source: nativeusage.Source{Path: "missing-history.jsonl", ThreadID: "thread", SessionID: "session", Provider: "codex"}}
	if err := r.cfg.Journal.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	// Registration must not resurrect a retained terminal collection.
	all, _ := r.cfg.Journal.UsageSources(t.Context())
	s = all[0]
	s.Collection = c
	if err := r.cfg.Journal.QueueUsage(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	var seen []protocol.Envelope
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var e protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&e)
		seen = append(seen, e)
		if fail {
			http.Error(w, "unconfirmed", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: e.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.nativeUsageSupported = true
	if err := r.syncNativeUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatal("old peer received collection operation", seen)
	}
	r.usageCollectionSupported = true
	if err := r.syncNativeUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.syncNativeUsage(t.Context()); err == nil {
		t.Fatal("unconfirmed report was treated as success")
	}
	pending, _ := r.cfg.Journal.Requests(t.Context())
	if len(pending) != 1 {
		t.Fatal(pending)
	}
	original := pending[0].Envelope
	fail = false
	if err := r.syncNativeUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[1].RequestID != original.RequestID || string(seen[1].Payload) != string(original.Payload) {
		t.Fatal("report identity changed", seen)
	}
	if pending, _ := r.cfg.Journal.Requests(t.Context()); len(pending) != 0 {
		t.Fatal(pending)
	}
	if pending, _ := r.cfg.Journal.PendingUsageSources(t.Context()); len(pending) != 0 {
		t.Fatal("retired source resumed reading", pending)
	}
}

func TestReadFailureDoesNotCommitEarlierFilesCursorsWithoutSamples(t *testing.T) {
	r := controlRunner(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "a.jsonl")
	child := filepath.Join(dir, "b.jsonl")
	a := `{"type":"session_meta","payload":{"id":"root","session_id":"root"}}` + "\n" + `{"type":"token_usage_record","ordinal":1,"payload":{"thread_id":"root","session_id":"root","turn_id":"turn","response_id":"request","usage":{"total_tokens":12}}}` + "\n"
	b := `{"type":"session_meta","payload":{"id":"child","session_id":"root"}}` + "\nnot-json\n"
	if err := os.WriteFile(root, []byte(a), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte(b), 0600); err != nil {
		t.Fatal(err)
	}
	s := journal.UsageSource{ID: "source", RunID: "run", RegistrationQueued: true, Source: nativeusage.Source{Path: root, Provider: "codex", ThreadID: "root", SessionID: "root"}}
	if err := r.cfg.Journal.RegisterUsageSource(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	r.nativeUsageSupported = true
	if err := r.syncNativeUsage(t.Context()); err == nil {
		t.Fatal("malformed second history unexpectedly succeeded")
	}
	all, err := r.cfg.Journal.UsageSources(t.Context())
	if err != nil || len(all) != 1 || len(all[0].Cursors) != 0 || all[0].Collection.RetryDeadlineAt == 0 {
		t.Fatal("failure advanced an uncommitted cursor", all, err)
	}
	if requests, _ := r.cfg.Journal.Requests(t.Context()); len(requests) != 0 {
		t.Fatal("failed page queued a partial upload", requests)
	}
}
