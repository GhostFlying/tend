package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func capabilityPathMatches(pattern, actual string) bool {
	want := strings.Split(pattern, "/")
	got := strings.Split(actual, "/")
	if len(want) != len(got) {
		return false
	}
	for i, p := range want {
		if strings.HasPrefix(p, "{") {
			continue
		}
		if p != got[i] {
			return false
		}
	}
	return true
}

func TestCapabilityBindingsExerciseRealDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("includes Unix private control sockets")
	}
	// Keep socket names within the platform's Unix path bound.
	dir, err := os.MkdirTemp("", "tend-cli-cap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	original := []byte("original file\x00\xff\n")
	digest := sha256.Sum256(original)
	var mu sync.Mutex
	var calls []recorded
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		mu.Unlock()
		if r.URL.Path == "/api/v1/events" || r.URL.Path == "/api/v1/messaging/events" {
			fmt.Fprint(w, "event: start\ndata: {\"event_seq\":1}\n\n")
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/v1/session" {
			json.NewEncoder(w).Encode(protocol.SessionView{Token: "owner-token", DeploymentID: "deployment-test"})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/v1/ops" {
			var env protocol.Envelope
			if err := json.Unmarshal(body, &env); err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			return
		}
		if r.URL.Path == "/api/v1/conversation/attachments" {
			json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			return
		}
		if strings.Contains(r.URL.Path, "webhook-keys") {
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/agents/register" || r.URL.Path == "/agents/registration" {
			json.NewEncoder(w).Encode(protocol.LocalAgentConfigurationResult{Registration: &protocol.LocalAgentRegistration{State: "registered"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/agents/") {
			fmt.Fprint(w, `{"current":{"revision":2,"state":"ready"},"request_state":"accepted"}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/archive/") {
			fmt.Fprint(w, `{"preparation":{"revision":2},"hosts":[]}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/upgrades/") {
			fmt.Fprint(w, `{"upgrade_id":"sample","revision":2,"status":"running"}`)
			return
		}
		download := r.URL.Path == "/api/v1/conversation/attachments/sample" || r.URL.Path == "/api/v1/files/sample" || strings.HasSuffix(r.URL.Path, "/images/sample/content") || r.URL.Path == "/api/v1/archives/sample/originals/sample/sample" || r.URL.Path == "/api/v1/archives/sample/resources/sample/files/1"
		if download {
			w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
			w.Header().Set("Content-Length", fmt.Sprint(len(original)))
			w.Write(original)
			return
		}
		fmt.Fprint(w, `{"revision":2,"control_revision":2,"version":2,"assignment_epoch":3,"issue_id":"sample","source":"followup","title":"Sample","plan":{"title":"Sample","stages":[]},"items":[],"tasks":[],"queue":[],"decisions":[],"hosts":[],"control":{"revision":2},"rule":{"revision":2},"upgrade":{"revision":2}}`)
	})
	remote := httptest.NewServer(handler)
	defer remote.Close()
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	local := &http.Server{Handler: handler}
	defer local.Close()
	go func() { _ = local.Serve(listener) }()
	input := filepath.Join(dir, "input.bin")
	secret := filepath.Join(dir, "key")
	if err = os.WriteFile(input, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(secret, bytes.Repeat([]byte{42}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range CapabilityBindings() {
		t.Run(entry.Capability+"/"+entry.Command, func(t *testing.T) {
			env := newTestEnv(t, remote.URL)
			output := filepath.Join(dir, "output.bin")
			_ = os.Remove(output)
			args := append([]string(nil), entry.Example...)
			for i, arg := range args {
				args[i] = strings.NewReplacer("{server}", remote.URL, "{socket}", socket, "{input}", input, "{secret}", secret, "{output}", output, "{release}", dir, "{request}", env.IDs.New(protocol.PrefixRequest)).Replace(arg)
			}
			mu.Lock()
			calls = nil
			mu.Unlock()
			if code := env.main(args...); code != exitOK {
				t.Fatalf("%v: exit=%d stderr=%s stdout=%s", args, code, env.errOut, env.out)
			}
			expected := strings.TrimPrefix(entry.Capability, "local ")
			mu.Lock()
			seen := append([]recorded(nil), calls...)
			mu.Unlock()
			matched := false
			for _, call := range seen {
				if method, path, ok := strings.Cut(expected, " "); ok {
					if call.method == method && capabilityPathMatches(path, call.path) {
						matched = true
					}
				} else if call.path == "/api/v1/ops" {
					var envelope protocol.Envelope
					if json.Unmarshal(call.body, &envelope) == nil && envelope.Operation == expected {
						matched = true
					}
				}
			}
			if !matched {
				t.Fatalf("binding did not exercise %s: %+v", expected, seen)
			}
			for _, call := range seen {
				if strings.HasPrefix(call.path, "/api/") && call.path != "/api/v1/session" && call.auth != "Bearer owner-token" {
					t.Fatalf("missing owner authentication: %+v", call)
				}
			}
			if raw, err := os.ReadFile(output); err == nil && !bytes.Equal(raw, original) {
				t.Fatalf("download changed original bytes: %q", raw)
			}
			if !strings.Contains(commandUsage(nil), entry.Command) {
				t.Fatalf("command is absent from help: %s", entry.Command)
			}
		})
	}
}

func TestCapabilityQueriesPreserveCompleteFiltersAndCursors(t *testing.T) {
	cases := []struct {
		args  []string
		path  string
		query url.Values
	}{
		{[]string{"conversation", "list", "--search", "A & B", "--status", "waiting", "--after", "page two"}, "/api/v1/agent-conversations", url.Values{"search": {"A & B"}, "status": {"waiting"}, "after": {"page two"}}},
		{[]string{"conversation", "show", "main", "--before", "50", "--limit", "20"}, "/api/v1/conversation", url.Values{"conversation_id": {"main"}, "before": {"50"}, "limit": {"20"}}},
		{[]string{"conversation", "window", "main", "12", "--offset", "4096"}, "/api/v1/agent-conversations/main/rollout/12", url.Values{"offset": {"4096"}}},
		{[]string{"message", "list", "--before", "0"}, "/api/v1/messaging/messages", url.Values{"before": {"0"}}},
		{[]string{"task", "list", "--all", "--lifecycle", "completed", "--sort", "completed_at", "--before", "1:task", "--limit", "10"}, "/api/v1/tasks", url.Values{"all": {"1"}, "lifecycle": {"completed"}, "sort": {"completed_at"}, "before": {"1:task"}, "limit": {"10"}}},
		{[]string{"usage", "--window", "month", "--offset", "-1", "--model", "model name", "--subject", "main", "--before", "45"}, "/api/v1/token-usage", url.Values{"window": {"month"}, "offset": {"-1"}, "model": {"model name"}, "subject": {"main"}, "before": {"45"}}},
		{[]string{"inbox", "list", "--include-archived", "--kind", "action", "--after", "cursor", "--limit", "10"}, "/api/v1/inbox", url.Values{"include_archived": {"true"}, "kind": {"action"}, "after": {"cursor"}, "limit": {"10"}}},
		{[]string{"archive", "records", "ledger", "messages", "--after", "cursor", "--limit", "2"}, "/api/v1/archives/ledger/records/messages", url.Values{"after": {"cursor"}, "limit": {"2"}}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f := newFakeServer(t)
			f.on("GET "+tc.path, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Query().Encode() != tc.query.Encode() {
					t.Errorf("query %s want %s", r.URL.RawQuery, tc.query.Encode())
				}
				fmt.Fprint(w, `{}`)
			})
			env := newTestEnv(t, f.srv.URL)
			if code := env.main(tc.args...); code != 0 {
				t.Fatal(code, env.errOut)
			}
		})
	}
}

func TestCapabilityWritesKeepOriginalIdentityAndUncertainty(t *testing.T) {
	for _, status := range []protocol.OpStatus{protocol.OpFailed, protocol.OpUnknown} {
		for _, cmd := range capabilityCommands {
			if cmd.Operation == "" {
				continue
			}
			t.Run(string(status)+"/"+cmd.Name, func(t *testing.T) {
				f := newFakeServer(t)
				f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
					var env protocol.Envelope
					if err := json.Unmarshal(body, &env); err != nil {
						t.Fatal(err)
					}
					op := &protocol.Operation{RequestID: env.RequestID, Status: status}
					if status == protocol.OpFailed {
						op.Error = protocol.Errorf(protocol.CodeStaleRevision, "changed")
					}
					json.NewEncoder(w).Encode(protocol.APIResponse{Operation: op})
				})
				env := newTestEnv(t, f.srv.URL)
				id := env.IDs.New(protocol.PrefixRequest)
				var args []string
				for _, entry := range CapabilityBindings() {
					if entry.Command == cmd.Name {
						args = append([]string(nil), entry.Example...)
						break
					}
				}
				args = append(args, "--request-id", id)
				if code := env.main(args...); code != exitError {
					t.Fatalf("uncertain/refused write returned %d: %s", code, env.errOut)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.requests) != 1 || f.requests[0].path != "/api/v1/ops" {
					t.Fatalf("recovery refreshed state or resent a known outcome: %+v", f.requests)
				}
				var sent protocol.Envelope
				json.Unmarshal(f.requests[0].body, &sent)
				if sent.RequestID != id {
					t.Fatal("original identity changed")
				}
				if cmd.RevisionRoute != "" && (sent.ExpectedControlRevision == nil || *sent.ExpectedControlRevision != 2) {
					t.Fatal("original control revision changed")
				}
			})
		}
	}
}

func TestCapabilityDryRunAndTransportRetryKeepExactEnvelope(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/issue-notification-settings", protocol.IssueNotificationSettings{Revision: 4, DelayMinutes: 2})
	env := newTestEnv(t, f.srv.URL)
	if code := env.main("issue", "notifications", "set", "--data", `{"delay_minutes":5}`, "--dry-run"); code != 0 {
		t.Fatal(code, env.errOut)
	}
	original := append([]byte(nil), env.out.Bytes()...)
	var envelope protocol.Envelope
	if err := json.Unmarshal(original, &envelope); err != nil || envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != 4 {
		t.Fatal(err, envelope)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("dry-run mutated state")
	}
	attempts := 0
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		attempts++
		var sent protocol.Envelope
		json.Unmarshal(body, &sent)
		if sent.RequestID != envelope.RequestID || sent.ExpectedControlRevision == nil || *sent.ExpectedControlRevision != 4 || !sameCapabilityPayload(t, &sent, &envelope) {
			t.Error("frozen request changed")
		}
		if attempts == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			connection.Close()
			return
		}
		json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: envelope.RequestID, Status: protocol.OpSucceeded}})
	})
	if code := env.main("request", "replay", "--data", string(original)); code != 0 {
		t.Fatal(code, env.errOut)
	}
	calls := f.seen("POST /api/v1/ops")
	if len(calls) != 2 || !bytes.Equal(calls[0].body, calls[1].body) {
		t.Fatal("transport retry changed the request")
	}
	if len(f.seen("GET /api/v1/issue-notification-settings")) != 1 {
		t.Fatal("replay refreshed control state")
	}
	if code := env.main("request", "replay", "--data", `{"request_id":"`+envelope.RequestID+`","operation":"runner.claim","target":{"type":"runner"}}`); code != exitUsage {
		t.Fatal("executor protocol exposed as owner operation")
	}
}

func TestCapabilityDoctorIsReadOnlyAndSeparatesDisabledAndUnavailable(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			f := newFakeServer(t)
			f.answer("GET /api/v1/deployment", map[string]string{"deployment_id": "deployment-test"})
			f.answer("GET /api/v1/session", protocol.SessionView{Login: "owner@example.com"})
			f.answer("GET /api/v1/snapshot", protocol.Snapshot{})
			f.answer("GET /api/v1/coordination/metrics", protocol.CoordinationMetrics{})
			if !unavailable {
				f.answer("GET /api/v1/conversation/settings", protocol.ConversationSettingsView{Enabled: false})
			}
			env := newTestEnv(t, f.srv.URL)
			want := exitOK
			if unavailable {
				want = exitError
			}
			if code := env.main("doctor", "--json"); code != want {
				t.Fatal(code, env.out, env.errOut)
			}
			var checks []diagnosticCheck
			if err := json.Unmarshal(env.out.Bytes(), &checks); err != nil {
				t.Fatal(err)
			}
			state := ""
			for _, check := range checks {
				if check.Name == "conversation" {
					state = check.Status
				}
			}
			if unavailable && state != "unavailable" || !unavailable && state != "disabled" {
				t.Fatal("disabled and unavailable collapsed", checks)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, call := range f.requests {
				if call.method != "GET" {
					t.Fatal("doctor performed a write", call)
				}
			}
		})
	}
}

func TestCapabilityFailedDownloadPreservesExistingDestination(t *testing.T) {
	for _, route := range []string{"/api/v1/files/sample", "/api/v1/archives/history/originals/attachment/sample"} {
		t.Run(route, func(t *testing.T) {
			f := newFakeServer(t)
			f.on("GET "+route, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				digest := sha256.Sum256([]byte("expected bytes"))
				w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
				w.Header().Set("Content-Length", "7")
				fmt.Fprint(w, "invalid")
			})
			env := newTestEnv(t, f.srv.URL)
			out := filepath.Join(t.TempDir(), "existing.bin")
			if err := os.WriteFile(out, []byte("retained original"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"file", "download", "sample"}
			if strings.Contains(route, "archives") {
				args = []string{"archive", "download", "history", "attachment", "sample"}
			}
			args = append(args, "--out", out)
			if code := env.main(args...); code != exitError {
				t.Fatal("corrupt download succeeded")
			}
			body, err := os.ReadFile(out)
			if err != nil || string(body) != "retained original" {
				t.Fatal("failed transfer overwrote original", string(body), err)
			}
		})
	}
}

func sameCapabilityPayload(t *testing.T, a, b *protocol.Envelope) bool {
	t.Helper()
	left, err := protocol.PayloadHash(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := protocol.PayloadHash(b)
	if err != nil {
		t.Fatal(err)
	}
	return left == right
}

func TestCapabilityNewListsHaveReadableAndCompleteJSONOutput(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/tasks", protocol.TaskPage{Tasks: []protocol.TaskSummary{{TaskID: "task-one", Title: "Review the complete contract", Lifecycle: protocol.TaskCompleted}}, Next: "next-page"})
	env := newTestEnv(t, f.srv.URL)
	if code := env.main("task", "list"); code != 0 || strings.HasPrefix(env.out.String(), "{") || !strings.Contains(env.out.String(), "Review the complete contract") || !strings.Contains(env.out.String(), "next-page") {
		t.Fatal(code, env.out, env.errOut)
	}
	env.out.Reset()
	if code := env.main("task", "list", "--json"); code != 0 {
		t.Fatal(code, env.errOut)
	}
	var page protocol.TaskPage
	if err := json.Unmarshal(env.out.Bytes(), &page); err != nil || len(page.Tasks) != 1 || page.Tasks[0].TaskID != "task-one" || page.Next != "next-page" {
		t.Fatal(page, err)
	}
}

func TestCapabilityLargeStdinAndExplicitUploadMetadata(t *testing.T) {
	f := newFakeServer(t)
	text := strings.Repeat("x", (1<<20)+100)
	raw, _ := json.Marshal(map[string]string{"body": text})
	env := newTestEnv(t, f.srv.URL)
	env.Stdin = bytes.NewReader(raw)
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var envelope protocol.Envelope
		json.Unmarshal(body, &envelope)
		var payload map[string]string
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload["body"] != text {
			t.Error("stdin payload was truncated", err)
		}
		json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{Status: protocol.OpAccepted}})
	})
	if code := env.main("conversation", "send", "main", "--file", "-"); code != 0 {
		t.Fatal(code, env.errOut)
	}
	original := []byte("original bytes\x00\xff")
	env.Stdin = bytes.NewReader(original)
	f.on("POST /api/v1/conversation/attachments", func(w http.ResponseWriter, r *http.Request, body []byte) {
		if !bytes.Equal(body, original) || r.URL.Query().Get("name") != "original.data" || r.URL.Query().Get("media_type") != "application/example" {
			t.Error("upload changed original content/metadata")
		}
		json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	})
	if code := env.main("attachment", "upload", "-", "--name", "original.data", "--media-type", "application/example"); code != 0 {
		t.Fatal(code, env.errOut)
	}
}

func TestCapabilityReadablePagesRetainBusinessIdentityAndOriginalContent(t *testing.T) {
	cases := []struct {
		args       []string
		path, body string
		required   []string
	}{
		{[]string{"inbox", "list"}, "/api/v1/inbox", `{"items":[{"attention_id":"attention-one","issue_id":"issue-other","title":"Original action"}]}`, []string{"attention-one", "issue-other", "Original action"}},
		{[]string{"conversation", "history", "main"}, "/api/v1/conversation", `{"items":[{"message_id":"message-one","body":"Original message body"}]}`, []string{"message-one", "Original message body"}},
		{[]string{"conversation", "queue"}, "/api/v1/conversation/queue", `{"items":[{"item":{"message_id":"queued-message","body":"Queued original body"},"revision":7}]}`, []string{"queued-message", "Queued original body", "7"}},
	}
	for _, tc := range cases {
		t.Run(tc.args[0]+"/"+tc.args[1], func(t *testing.T) {
			f := newFakeServer(t)
			f.answer("GET "+tc.path, json.RawMessage(tc.body))
			env := newTestEnv(t, f.srv.URL)
			if code := env.main(tc.args...); code != 0 {
				t.Fatal(code, env.errOut)
			}
			for _, part := range tc.required {
				if !strings.Contains(env.out.String(), part) {
					t.Fatalf("lost %q from readable output: %s", part, env.out)
				}
			}
		})
	}
}

func TestCapabilityDoctorReportsPendingLocalReload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix private control")
	}
	f := newFakeServer(t)
	f.answer("GET /api/v1/deployment", map[string]string{"deployment_id": "deployment-test"})
	f.answer("GET /api/v1/session", protocol.SessionView{})
	f.answer("GET /api/v1/snapshot", protocol.Snapshot{})
	f.answer("GET /api/v1/coordination/metrics", protocol.CoordinationMetrics{})
	f.answer("GET /api/v1/conversation/settings", protocol.ConversationSettingsView{Enabled: false})
	dir, err := os.MkdirTemp("", "tend-cli-doctor-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	pending := "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	local := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/agents/status" {
			t.Error("doctor mutated local state")
		}
		json.NewEncoder(w).Encode(protocol.LocalAgentConfigurationResult{Current: &protocol.LocalAgentConfiguration{State: "local_accepted", Revision: 2, PendingRequestID: pending}})
	})}
	defer local.Close()
	go func() { _ = local.Serve(listener) }()
	env := newTestEnv(t, f.srv.URL)
	if code := env.main("doctor", "--socket", socket, "--json"); code != exitError {
		t.Fatal("pending reload reported success", code, env.out, env.errOut)
	}
	var checks []diagnosticCheck
	if err := json.Unmarshal(env.out.Bytes(), &checks); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range checks {
		if check.Name == "local-agents" {
			found = true
			if check.Status != "abnormal" {
				t.Fatal(check)
			}
		}
	}
	if !found || !strings.Contains(env.out.String(), pending) {
		t.Fatal("pending reconciliation identity is missing", env.out)
	}
	env.out.Reset()
	if code := env.main("doctor", "--socket", socket); code != exitError || !strings.Contains(env.out.String(), pending) {
		t.Fatal("readable diagnosis lost pending request identity", code, env.out)
	}

}

func TestCapabilityRecentMessageModeNeverSilentlyIgnoresFilters(t *testing.T) {
	for _, mode := range [][]string{{"--latest"}, {"--before", "0"}} {
		for _, filter := range []string{"--channel", "--conversation", "--issue", "--after"} {
			t.Run(strings.Join(mode, " ")+filter, func(t *testing.T) {
				f := newFakeServer(t)
				env := newTestEnv(t, f.srv.URL)
				args := append([]string{"message", "list"}, mode...)
				value := "sample"
				if filter == "--after" {
					value = "0"
				}
				args = append(args, filter, value)
				if code := env.main(args...); code != exitUsage || !strings.Contains(env.errOut.String(), "unfiltered recent-history mode") {
					t.Fatal(code, env.errOut)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.requests) != 0 {
					t.Fatal("unsupported filter reached the server", f.requests)
				}
			})
		}
	}
}

func TestCapabilityLeafHelpUsesActualFlagDefinitions(t *testing.T) {
	cases := []struct {
		args     []string
		required []string
	}{
		{[]string{"issue", "list", "--help"}, []string{"board-status", "assignee-kind", "assignee-ref", "include-closed", "limit", "project"}},
		{[]string{"logs", "--help"}, []string{"-f", "json"}},
		{[]string{"conversation", "handoff", "--help"}, []string{"prepare-only", "request-id", "status"}},
		{[]string{"message", "list", "--help"}, []string{"before", "after", "latest", "channel", "conversation"}},
		{[]string{"agents", "local", "reload", "--help"}, []string{"socket", "request-id", "revision"}},
		{[]string{"help", "issue", "list"}, []string{"board-status", "assignee-kind", "revision"}},
		{[]string{"schedule", "--help"}, []string{"schedule create", "schedule history"}},
		{[]string{"issue", "followup", "--help"}, []string{"followup create", "followup pause"}},
		{[]string{"planner", "--help"}, []string{"planner list", "planner put"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f := newFakeServer(t)
			env := newTestEnv(t, f.srv.URL)
			if code := env.main(tc.args...); code != exitOK {
				t.Fatal(code, env.out, env.errOut)
			}
			for _, flag := range tc.required {
				if !strings.Contains(env.out.String(), flag) {
					t.Fatalf("help lost flag %s: %s", flag, env.out)
				}
			}
			if env.errOut.Len() != 0 {
				t.Fatal("help leaked to stderr", env.errOut)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.requests) != 0 {
				t.Fatal("help contacted server", f.requests)
			}
		})
	}
}

func TestCapabilityEveryRegisteredLeafAndAliasCanShowHelpWithoutArguments(t *testing.T) {
	commands := map[string]bool{}
	for _, entry := range CapabilityBindings() {
		commands[entry.Command] = true
	}
	for _, command := range additionalHelpCommands {
		commands[command] = true
	}
	for command := range commands {
		t.Run(command, func(t *testing.T) {
			f := newFakeServer(t)
			env := newTestEnv(t, f.srv.URL)
			args := append(strings.Fields(command), "--help")
			if code := env.main(args...); code != exitOK || env.out.Len() == 0 {
				t.Fatalf("help unavailable: %d out=%s err=%s", code, env.out, env.errOut)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.requests) != 0 {
				t.Fatal("help contacted server", f.requests)
			}
		})
	}
}

func TestCapabilityIssueFiltersRejectUnsupportedCombinationsBeforeHTTP(t *testing.T) {
	for _, flags := range [][]string{{"--board", "--parent", "sample"}, {"--board-status", "in_progress"}, {"--board", "--assignee-kind", "agent"}, {"--board", "--assignee-ref", "sample"}, {"--assignee-kind", "agent", "--assignee-ref", "sample"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			f := newFakeServer(t)
			env := newTestEnv(t, f.srv.URL)
			args := append([]string{"issue", "list"}, flags...)
			if code := env.main(args...); code != exitUsage {
				t.Fatal("ignored filter was accepted", code, env.out, env.errOut)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.requests) != 0 {
				t.Fatal("unsupported filter reached API", f.requests)
			}
		})
	}
	f := newFakeServer(t)
	f.on("GET /api/v1/issues", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		query := r.URL.Query()
		if query.Get("board") != "true" || query.Get("board_status") != "in_progress" || query.Get("assignee_kind") != "agent" || query.Get("assignee_ref") != "agent-one" {
			t.Error("valid board filters changed", query)
		}
		json.NewEncoder(w).Encode(protocol.IssuePage{})
	})
	env := newTestEnv(t, f.srv.URL)
	if code := env.main("issue", "list", "--board", "--board-status", "in_progress", "--assignee-kind", "agent", "--assignee-ref", "agent-one", "--json"); code != 0 {
		t.Fatal(code, env.errOut)
	}
}

func TestCapabilityImageShowReadsCompleteLargeResponse(t *testing.T) {
	f := newFakeServer(t)
	data := strings.Repeat("A", (16<<20)+4096)
	f.answer("GET /api/v1/conversation/images/large-image", protocol.ConversationImageResponse{Image: protocol.ConversationImageData{DataBase64: data}})
	env := newTestEnv(t, f.srv.URL)
	if code := env.main("image", "show", "large-image", "--json"); code != exitOK {
		t.Fatal("valid image response was truncated", code, env.errOut)
	}
	var response protocol.ConversationImageResponse
	if err := json.Unmarshal(env.out.Bytes(), &response); err != nil || response.Image.DataBase64 != data {
		t.Fatal("image content changed or response was truncated", err, len(response.Image.DataBase64))
	}
}

func TestCapabilityFollowupBindingsSelectTheCorrectBusinessTarget(t *testing.T) {
	for _, action := range []string{"create", "update", "pause", "resume", "cancel", "trigger"} {
		t.Run(action, func(t *testing.T) {
			f := newFakeServer(t)
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
				var envelope protocol.Envelope
				json.Unmarshal(body, &envelope)
				target := protocol.Target{Type: "followup", ID: "rule-one"}
				if action == "create" {
					target = protocol.Target{Type: "issue", ID: "issue-one"}
				}
				if envelope.Operation != "issues.followups_"+action || envelope.Target != target || envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != 2 {
					t.Error("wrong followup contract", envelope)
				}
				var payload protocol.IssueFollowupPayload
				if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.ExpectedAssignmentEpoch != 3 {
					t.Error(payload, err)
				}
				json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: envelope.RequestID, Status: protocol.OpSucceeded}})
			})
			env := newTestEnv(t, f.srv.URL)
			args := []string{"issue", "followup", action, "issue-one"}
			if action != "create" {
				args = append(args, "rule-one")
			}
			args = append(args, "--revision", "2", "--epoch", "3", "--data", `{}`)
			if code := env.main(args...); code != 0 {
				t.Fatal(code, env.errOut)
			}
			found := false
			for _, entry := range CapabilityBindings() {
				if entry.Capability == "issues.followups_"+action && entry.Command == "issue followup "+action {
					found = true
				}
			}
			if !found {
				t.Fatal("canonical followup command is missing from capability inventory")
			}
		})
	}
}

func TestCapabilityStdinUploadCancellationReleasesInput(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); _, _ = io.Copy(io.Discard, r.Body) }))
	defer server.Close()
	env := newTestEnv(t, server.URL)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	env.Stdin = reader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- Main(ctx, env.Env, []string{"attachment", "upload", "-", "--name", "original.bin"}) }()
	producer := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("original prefix")); producer <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		reader.Close()
		t.Fatal("upload did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != exitError {
			t.Fatal("cancelled upload reported success", code)
		}
	case <-time.After(3 * time.Second):
		reader.Close()
		<-done
		t.Fatal("cancelled upload left stdin blocked")
	}
	if err := <-producer; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestCapabilityLocalRepositoryBindingPreservesIdentityAndRevision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix private operator socket")
	}
	dir, err := os.MkdirTemp("", "tend-cli-repo-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []protocol.BindLocalRepositoryRequest
	reads := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Error("owner token leaked to private control socket")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /repositories":
			reads++
			json.NewEncoder(w).Encode(protocol.LocalRepositoryStatus{Revision: 7})
		case "POST /repositories/bind":
			var request protocol.BindLocalRepositoryRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			requests = append(requests, request)
			json.NewEncoder(w).Encode(protocol.LocalRepositoryStatus{Revision: 8})
		default:
			t.Error("unexpected operator route", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	env := newTestEnv(t, "http://unused.invalid")
	binding := `{"repo":"project","remote_url":"https://example.com/project.git","orca":"id:existing"}`
	args := []string{"repo", "local", "bind", "--socket", socket, "--data", binding}
	if code := env.main(args...); code != 0 {
		t.Fatal(code, env.errOut)
	}
	mu.Lock()
	if reads != 1 || len(requests) != 1 || requests[0].ExpectedRevision != 7 || requests[0].Binding.Repo != "project" || requests[0].Binding.Orca != "id:existing" {
		t.Fatal("binding contract changed", reads, requests)
	}
	original := requests[0]
	mu.Unlock()
	retry := append(append([]string(nil), args...), "--request-id", original.RequestID, "--revision", "7")
	if code := env.main(retry...); code != 0 {
		t.Fatal(code, env.errOut)
	}
	mu.Lock()
	if reads != 1 || len(requests) != 2 || requests[0] != requests[1] {
		t.Fatal("retry refreshed the revision or changed input", reads, requests)
	}
	mu.Unlock()
	if code := env.main(append(append([]string(nil), args...), "--request-id", original.RequestID)...); code != exitUsage {
		t.Fatal("retry without original revision was accepted", code)
	}
	env.out.Reset()
	if code := env.main(append(retry, "--dry-run")...); code != 0 {
		t.Fatal(code, env.errOut)
	}
	var preview struct {
		Method  string                              `json:"method"`
		Path    string                              `json:"path"`
		Request protocol.BindLocalRepositoryRequest `json:"request"`
	}
	if err := json.Unmarshal(env.out.Bytes(), &preview); err != nil || preview.Request != original || preview.Path != "/repositories/bind" {
		t.Fatal("dry-run changed original request", preview, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 || len(requests) != 2 {
		t.Fatal("dry-run performed a request", reads, requests)
	}
}

func TestCapabilityJSONStdinCancellationStopsBeforeMutation(t *testing.T) {
	f := newFakeServer(t)
	env := newTestEnv(t, f.srv.URL)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	env.Stdin = reader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- Main(ctx, env.Env, []string{"issue", "create", "--file", "-"}) }()
	if _, err := writer.Write([]byte(`{"plan":`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case code := <-done:
		if code != exitError {
			t.Fatal("interrupted input reported success", code)
		}
	case <-time.After(3 * time.Second):
		reader.Close()
		<-done
		t.Fatal("JSON input remained blocked after cancellation")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("partial JSON input created a mutation", f.requests)
	}
}
