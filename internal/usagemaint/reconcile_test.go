package usagemaint

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestReconcilePreviewAndDurableFailedReceipt(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := `{"type":"session_meta","payload":{"id":"registered","session_id":"session","cli_version":"test"}}
{"type":"token_usage_record","ordinal":1,"timestamp":"2026-01-01T00:00:00Z","payload":{"thread_id":"registered","session_id":"session","turn_id":"turn","response_id":"response","usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}
`
	if err := os.WriteFile(filepath.Join(dir, "rollout-registered.jsonl"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var requests []string
	fail := true
	stopped := false
	applied := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/api/v1/token-usage/sources":
			_ = json.NewEncoder(w).Encode([]protocol.UsageTarget{{ConversationID: "main", ThreadID: "registered", Stopped: stopped}})
		case "/api/v1/token-usage/preview":
			var upload protocol.UsageUpload
			if err := json.NewDecoder(r.Body).Decode(&upload); err != nil {
				t.Error(err)
			}
			result := protocol.UsagePreview{}
			for _, sample := range upload.Samples {
				result.Records++
				if applied[sample.RecordID] {
					result.Existing++
				} else {
					result.New++
					result.TokenDelta += *sample.Total
				}
			}
			_ = json.NewEncoder(w).Encode(result)
		case "/api/v1/ops":
			var env protocol.Envelope
			if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
				t.Error(err)
			}
			requests = append(requests, env.RequestID)
			op := protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}
			if fail {
				op.Status = protocol.OpFailed
				op.Error = protocol.Errorf(protocol.CodeInvalidRequest, "review required")
			} else {
				var input protocol.UsageReconcile
				if err := json.Unmarshal(env.Payload, &input); err != nil {
					t.Error(err)
				}
				for _, s := range input.Samples {
					applied[s.RecordID] = true
				}
			}
			_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &op})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := Config{Server: server.URL, Token: "test", Provider: "traex", NativeHome: home, Journal: filepath.Join(t.TempDir(), "journal.sqlite"), Now: func() time.Time { return time.Unix(100, 0) }}
	var output bytes.Buffer
	if err := Run(t.Context(), config, &output); err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Preview.TokenDelta != 110 || len(requests) != 0 {
		t.Fatal(result, requests)
	}
	if _, err := os.Stat(config.Journal); !os.IsNotExist(err) {
		t.Fatal("preview created a journal", err)
	}
	config.Apply = true
	if err := Run(t.Context(), config, &output); err == nil {
		t.Fatal("failed operation receipt was acknowledged")
	}
	j, err := journal.Open(t.Context(), config.Journal, config.Now)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := j.Requests(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	fail = false
	output.Reset()
	if err = Run(t.Context(), config, &output); err != nil {
		t.Fatal(err)
	}
	if len(requests) < 2 || requests[0] != requests[1] || len(applied) != 1 {
		t.Fatal(requests, applied)
	}
	output.Reset()
	if err = Run(t.Context(), config, &output); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Preview.New != 0 || result.Preview.TokenDelta != 0 || result.Preview.Existing != 1 {
		t.Fatal(result)
	}
	stopped = true
	output.Reset()
	if err = Run(t.Context(), config, &output); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(t.Context(), config.Journal, config.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	sources, err := j.UsageSources(t.Context())
	if err != nil || len(sources) != 1 || !sources[0].WriterStopped || sources[0].Coverage != "caught_up" || sources[0].Collection.RetirementReason != "complete" {
		t.Fatal("standalone journal discarded the newly confirmed stop", sources, err)
	}
}
