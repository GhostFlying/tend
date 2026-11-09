package usagemaint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestLiveUsageApplyUsesOriginalWriterAndReplaysExactPendingEnvelope(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) { testLiveUsageApply(t, stopped, false) })
	}
	t.Run("append-stop-between-pages", func(t *testing.T) { testLiveUsageApply(t, false, true) })
}

func testLiveUsageApply(t *testing.T, stopped, interrupt bool) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"session_meta","payload":{"id":"registered","session_id":"session","cli_version":"test"}}` + "\n"
	for i := range 600 {
		data += fmt.Sprintf(`{"type":"token_usage_record","ordinal":%d,"timestamp":"2026-01-01T00:00:00Z","payload":{"thread_id":"registered","session_id":"session","turn_id":"turn","response_id":"response-%d","usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}`+"\n", i+1, i)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "rollout-registered.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(t.TempDir(), "original-journal.db")
	j, err := journal.Open(t.Context(), journalPath, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	original := protocol.Envelope{RequestID: "req_01KDVDNA0048R1ZE6R55WDNW0Y", Operation: protocol.OpRunnerUsageUpload, Target: protocol.Target{Type: "host", ID: "host"}, Payload: json.RawMessage(`{"run_id":"original","samples":[]}`)}
	unrelated := protocol.Envelope{RequestID: "req_01KDVDNA0048R1ZE6R55WDNW0Z", Operation: protocol.OpRunsReportStarted, Target: protocol.Target{Type: "run", ID: "unresolved-run"}, Payload: json.RawMessage(`{}`)}
	for _, env := range []protocol.Envelope{original, unrelated} {
		if err := j.PutRequest(t.Context(), journal.Request{Envelope: env}); err != nil {
			t.Fatal(err)
		}
	}
	if !stopped {
		src, err := nativeusage.Locate(t.Context(), home, "traex", "registered")
		if err != nil {
			t.Fatal(err)
		}
		if err := j.RegisterUsageSource(t.Context(), journal.UsageSource{ID: "active-source", RunID: "original", RegistrationQueued: true, Source: src}); err != nil {
			t.Fatal(err)
		}
		if err := j.ActivateUsageSource(t.Context(), "active-source", "ongoing-turn"); err != nil {
			t.Fatal(err)
		}
	}
	fail := true
	lateStopped := false
	var uploads []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runner/v1/usage/pending":
			pending, err := j.Requests(r.Context())
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(pending)
		case "/runner/v1/usage/source":
			var source journal.UsageSource
			if err := json.NewDecoder(r.Body).Decode(&source); err != nil {
				t.Error(err)
			}
			snapshot, err := j.RegisterUsageMaintenanceSource(r.Context(), source, false)
			if err != nil {
				t.Error(err)
			}

			_ = json.NewEncoder(w).Encode(snapshot)
		case "/runner/v1/usage/queue":
			var input journal.UsageQueueRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			input.Source.ExpectedRevision = input.ExpectedRevision
			snapshot, err := j.QueueUsageSnapshot(r.Context(), input.Source, input.Envelope)
			if err != nil {
				http.Error(w, "stale snapshot", http.StatusConflict)
				return
			}
			if interrupt && !lateStopped {
				lateStopped = true
				f, err := os.OpenFile(filepath.Join(home, "sessions", "rollout-registered.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Error(err)
					return
				}
				_, err = f.WriteString(`{"type":"token_usage_record","ordinal":601,"timestamp":"2026-01-01T00:00:01Z","payload":{"thread_id":"registered","session_id":"session","turn_id":"turn","response_id":"late-response","usage":{"total_tokens":12}}}` + "\n")
				_ = f.Close()
				if err != nil {
					t.Error(err)
				}
				if err := j.StopUsageSource(r.Context(), "original", "registered"); err != nil {
					t.Error(err)
				}
			}

			_ = json.NewEncoder(w).Encode(snapshot)
		case "/runner/v1/rpc":
			var env protocol.Envelope
			if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
				t.Error(err)
			}
			var result any
			switch env.Operation {
			case protocol.OpRunnerUsageSources:
				result = []protocol.UsageTarget{{RunID: "original", ThreadID: "registered", Provider: "traex", Stopped: stopped || lateStopped}}
			case protocol.OpRunnerUsagePreview:
				result = protocol.UsagePreview{}
			case protocol.OpRunnerUsageUpload:
				uploads = append(uploads, env)
				if fail {
					http.Error(w, "lost upload reply", http.StatusServiceUnavailable)
					return
				}
				if err := j.DeleteRequest(r.Context(), env.RequestID); err != nil {
					t.Error(err)
				}
			default:
				t.Error("unrelated native operation escaped usage maintenance", env.Operation)
			}
			raw, _ := json.Marshal(result)
			_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
		default:
			t.Error("unexpected live writer route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := Config{Server: server.URL, Token: "test", HostID: "host", Provider: "traex", NativeHome: home, Journal: journalPath, Apply: true, LiveJournal: true}
	var output bytes.Buffer
	if err := Run(t.Context(), config, &output); err == nil {
		t.Fatal("lost reply was treated as committed")
	}
	fail = false
	err = Run(t.Context(), config, &output)
	if interrupt {
		if err == nil {
			t.Fatal("buffered pre-stop EOF was committed as complete")
		}
		pendingSources, e := j.PendingUsageSources(t.Context())
		if e != nil || len(pendingSources) != 1 || !pendingSources[0].WriterStopped || pendingSources[0].Coverage == "caught_up" {
			t.Fatal("late usage lost its final read", pendingSources, e)
		}
		output.Reset()
		err = Run(t.Context(), config, &output)
	}
	if err != nil {
		t.Fatal("live maintenance failed", err)
	}

	var records int
	unique := map[string]bool{}
	for _, uploaded := range uploads[2:] {
		var input protocol.UsageUpload
		if err := json.Unmarshal(uploaded.Payload, &input); err != nil {
			t.Fatal(err)
		}
		records += len(input.Samples)
		for _, sample := range input.Samples {
			unique[sample.RecordID] = true
		}
	}
	if (!interrupt && records != 600) || (interrupt && len(unique) != 601) {
		t.Fatal("foreground pagination lost records", records)
	}
	if len(uploads) < 3 || !reflect.DeepEqual(uploads[0], original) || !reflect.DeepEqual(uploads[1], original) {
		t.Fatal("original pending request was replaced or silently skipped", uploads)
	}
	pending, err := j.Requests(t.Context())
	if err != nil || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, unrelated) {
		t.Fatal("usage reconciliation changed unrelated uncertainty", pending, err)
	}
	sources, err := j.UsageSources(t.Context())
	if err != nil || len(sources) != 1 || len(sources[0].Cursors) == 0 || ((stopped || lateStopped) && sources[0].Coverage != "caught_up") || (!stopped && !lateStopped && sources[0].Collection.State != protocol.UsageActive) {
		t.Fatal("cursor and usage receipt did not commit in the original writer", sources, err)
	}
}
