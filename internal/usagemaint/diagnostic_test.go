package usagemaint

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestLiveUsageRejectionIdentifiesStageAndOriginalRequestWithoutErrorBody(t *testing.T) {
	for _, failedStage := range []string{"register_source", "queue_usage"} {
		t.Run(failedStage, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, "sessions"), 0700); err != nil {
				t.Fatal(err)
			}
			data := `{"type":"session_meta","payload":{"id":"registered","session_id":"session","cli_version":"test"}}
{"type":"token_usage_record","ordinal":1,"payload":{"thread_id":"registered","session_id":"session","turn_id":"turn","response_id":"response","usage":{"total_tokens":12}}}
`
			if err := os.WriteFile(filepath.Join(home, "sessions", "rollout-registered.jsonl"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var source journal.UsageSource
			var originalRequest string
			calls, uploads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/runner/v1/usage/pending":
					_ = json.NewEncoder(w).Encode([]journal.Request{})
				case "/runner/v1/usage/source":
					if err := json.NewDecoder(r.Body).Decode(&source); err != nil {
						t.Error(err)
					}
					if failedStage != "register_source" {
						source.Collection = protocol.NewUsageCollection(false, true)
						_ = json.NewEncoder(w).Encode(journal.UsageSourceSnapshot{Source: source, ExpectedRevision: source.Collection.Revision})
						return
					}
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: protocol.Errorf(protocol.CodeForbidden, "DO_NOT_EXPOSE_INTERNAL_SQL_OR_CREDENTIAL")})
				case "/runner/v1/usage/queue":
					var input journal.UsageQueueRequest
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					originalRequest = input.Envelope.RequestID
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: protocol.Errorf(protocol.CodeStaleRevision, "DO_NOT_EXPOSE_INTERNAL_SQL_OR_CREDENTIAL")})
				case "/runner/v1/rpc":
					var envelope protocol.Envelope
					if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
						t.Error(err)
					}
					var value any
					switch envelope.Operation {
					case protocol.OpRunnerUsageSources:
						value = []protocol.UsageTarget{{RunID: "original-run", ConversationID: "issue:original", ThreadID: "registered", Provider: "traex", Stopped: true}}
					case protocol.OpRunnerUsagePreview:
						value = protocol.UsagePreview{}
					default:
						uploads++
						t.Error("failed source/queue must not upload", envelope.Operation)
					}
					raw, _ := json.Marshal(value)
					_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: envelope.RequestID, Status: protocol.OpSucceeded, Result: raw}})
				default:
					t.Error("unexpected route", r.URL.Path)
				}
			}))
			defer server.Close()
			config := Config{Server: server.URL, Token: "secret", HostID: "host", Provider: "traex", NativeHome: home, Apply: true, LiveJournal: true}
			var out bytes.Buffer
			err := Run(t.Context(), config, &out)
			if err == nil {
				t.Fatal("rejected original source was treated as complete")
			}
			for _, text := range []string{"stage=" + failedStage, `source_id="` + source.ID + `"`, `run_id="original-run"`, `thread_id="registered"`, "http_status=409"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatal("actual rejection context lost", err, text)
				}
			}
			if strings.Contains(err.Error(), "DO_NOT_EXPOSE") || strings.Contains(err.Error(), "secret") || uploads != 0 || calls > 5 {
				t.Fatal("unsafe error body exposed or failed mutation retried", err, uploads, calls)
			}
			if failedStage == "queue_usage" && (originalRequest == "" || !strings.Contains(err.Error(), originalRequest) || !strings.Contains(err.Error(), "code=stale_revision")) {
				t.Fatal("original upload identity or safe CAS code lost", originalRequest, err)
			}
		})
	}
}
