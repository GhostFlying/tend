package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueContextReaderPreservesQueryPagingAndProvenanceWithoutObservationScope(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			as := protocol.Assignment{Role: role, RunID: "run_current", TaskID: "task_original", RunToken: "current-run-only", Spec: json.RawMessage(`{}`)}
			calls := 0
			query := protocol.IssueMaterialsQuery{IssueID: "issue/related&scope", Kind: "files", ID: "file/exact&name", After: "opaque&cursor?+", Limit: 7}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/runner/v1/context" || r.Header.Get(headerRun) != as.RunID || r.Header.Get("Authorization") != "Bearer "+as.RunToken || r.URL.Query().Get("issue_id") != query.IssueID || r.URL.Query().Get("kind") != query.Kind || r.URL.Query().Get("id") != query.ID || r.URL.Query().Get("after") != query.After || r.URL.Query().Get("limit") != "7" {
					t.Error("context request changed its current Run or explicit page query", r.URL)
				}
				_ = json.NewEncoder(w).Encode(protocol.RunContext{RunID: as.RunID, TaskID: as.TaskID, Role: role, Materials: protocol.IssueMaterials{Kind: query.Kind, ObservedAt: 17, Seq: int64(calls), More: true, Next: "next-page", Files: []protocol.IssueMaterialFile{{ArtifactFile: protocol.ArtifactFile{FileID: "file_original", SHA256: "original"}, Sources: []protocol.IssueMaterialSource{{Kind: "background", Status: "history", ContentRevision: 4}}}}}})
			}))
			defer server.Close()
			r := New(Config{ServerURL: server.URL, HTTPClient: server.Client()})
			if r.observationReader(t.Context(), as) != nil {
				t.Fatal("ordinary context changed the legacy observation gate")
			}
			read := r.contextReader(t.Context(), as)
			for i := 1; i <= 2; i++ {
				got, err := read(t.Context(), query)
				if err != nil || got.Materials.Seq != int64(i) || !got.Materials.More || got.Materials.Next != "next-page" || got.Materials.Files[0].Sources[0].ContentRevision != 4 || got.Materials.Files[0].Sources[0].Status != "history" {
					t.Fatal("context was cached, flattened or changed", got, err)
				}
			}
		})
	}
}

func TestIssueContextToolPreservesPublicServerErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		code    protocol.Code
		message string
		plain   bool
		private string
	}{
		{"invalid query", http.StatusBadRequest, protocol.CodeInvalidRequest, "unsupported kind; use overview, messages, files or task_capabilities", false, ""},
		{"Issue denial", http.StatusForbidden, protocol.CodeForbidden, "requested Task is not associated with this Issue", false, ""},
		{"missing original", http.StatusNotFound, protocol.CodeNotFound, "requested original does not exist in this Issue", false, ""},
		{"old server", http.StatusNotFound, protocol.CodeNotFound, "does not provide", true, ""},
		{"internal response", http.StatusInternalServerError, protocol.CodeInternal, "server reported an internal failure; result is unconfirmed", false, "secret server path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				if tc.plain {
					_, _ = w.Write([]byte("404 page not found"))
					return
				}
				message := tc.message
				if tc.private != "" {
					message = tc.private
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: &protocol.Error{Code: tc.code, Message: message}})
			}))
			defer server.Close()
			r := New(Config{ServerURL: server.URL, HTTPClient: server.Client()})
			results := harness.Results{Spec: harness.WorkerSpec{Spec: json.RawMessage(`{}`), ReadContext: r.contextReader(t.Context(), protocol.Assignment{RunID: "run", Role: protocol.RoleImplement})}}
			got := results.Handle(t.Context(), harness.ToolCall{Tool: "read_context", Arguments: json.RawMessage(`{"kind":"unknown"}`)})
			if got.Success || !strings.Contains(got.Text, string(tc.code)) || !strings.Contains(got.Text, tc.message) || !strings.Contains(got.Text, "HTTP "+strconv.Itoa(tc.status)) || strings.Contains(got.Text, "secret") || calls != 1 {
				t.Fatal("public rejection was hidden as generic unavailability", got, calls)
			}
		})
	}
}

func TestIssueContextReaderFailureDoesNotInventAnEmptySuccessfulPage(t *testing.T) {
	for _, scenario := range []string{"old server", "forbidden", "rate limited", "unavailable", "malformed", "wrong run", "wrong task", "wrong role"} {
		t.Run(scenario, func(t *testing.T) {
			as := protocol.Assignment{Role: protocol.RoleImplement, RunID: "run_current", TaskID: "task_original", RunToken: "current-run-only"}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				out := protocol.RunContext{RunID: as.RunID, TaskID: as.TaskID, Role: as.Role}
				switch scenario {
				case "old server":
					http.Error(w, "current-run-only", http.StatusNotFound)
					return
				case "forbidden":
					http.Error(w, "current-run-only", http.StatusForbidden)
					return
				case "rate limited":
					http.Error(w, "current-run-only", http.StatusTooManyRequests)
					return
				case "unavailable":
					http.Error(w, "current-run-only", http.StatusServiceUnavailable)
					return
				case "malformed":
					_, _ = w.Write([]byte(`{`))
					return
				case "wrong run":
					out.RunID = "other"
				case "wrong task":
					out.TaskID = "other"
				case "wrong role":
					out.Role = protocol.RoleAccept
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			r := New(Config{ServerURL: server.URL, HTTPClient: server.Client()})
			if _, err := r.contextReader(t.Context(), as)(t.Context(), protocol.IssueMaterialsQuery{}); err == nil || strings.Contains(err.Error(), as.RunToken) || calls != 1 {
				t.Fatal("unconfirmed context was retried, accepted or leaked a body", err, calls)
			}
		})
	}
}

func TestIssueContextReaderCancelsWithRunLifetime(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	r := New(Config{ServerURL: server.URL, HTTPClient: server.Client()})
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	read := r.contextReader(runCtx, protocol.Assignment{RunID: "run", Role: protocol.RoleImplement})
	result := make(chan error, 1)
	go func() { _, err := read(t.Context(), protocol.IssueMaterialsQuery{}); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary context read outlived its Run")
	}
	if _, err := read(t.Context(), protocol.IssueMaterialsQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled Run issued another read", err)
	}
}
