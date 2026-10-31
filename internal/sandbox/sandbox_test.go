package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
	"github.com/GhostFlying/tend/internal/testkit"
)

type webClient struct {
	t       *testing.T
	url     string
	session protocol.SessionView
	http    *http.Client
}

func (c *webClient) call(method, path string, body, out any) {
	c.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(c.t.Context(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, c.url+path, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+c.session.Token)
	request.Header.Set("X-Tend-Deployment-ID", c.session.DeploymentID)
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		c.t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
	}
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			c.t.Fatal(err)
		}
	}
}
func (c *webClient) operation(id, name string, target protocol.Target, revision *int64, payload any) {
	c.t.Helper()
	raw, _ := json.Marshal(payload)
	var result protocol.APIResponse
	c.call(http.MethodPost, "/api/v1/ops", protocol.Envelope{RequestID: seedRequest(id), Operation: name, Target: target, ExpectedControlRevision: revision, Payload: raw}, &result)
	if result.Operation == nil || result.Operation.Status == protocol.OpFailed {
		c.t.Fatalf("%s: %+v", name, result.Operation)
	}
}
func waitFor(t *testing.T, ctx context.Context, label string, check func() bool) {
	t.Helper()
	for !check() {
		if !delay(ctx, 150*time.Millisecond) {
			t.Fatalf("timed out: %s", label)
		}
	}
}

func TestSandboxRealOwnerAPIAndRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ready := make(chan Demo, 1)
	runtime := make(chan *serverkit.Runtime, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Data: root, Listener: listener, Ready: func(r *serverkit.Runtime, demo Demo) { runtime <- r; ready <- demo }}, slog.New(slog.NewTextHandler(testLog{t}, nil)))
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("sandbox goroutines did not stop")
		}
	}()
	var demo Demo
	select {
	case demo = <-ready:
	case err := <-done:
		done <- err
		t.Fatalf("sandbox startup: %v", err)
	case <-ctx.Done():
		t.Fatal("sandbox seed timed out")
	}
	r := <-runtime
	c := webClient{t: t, url: "http://" + listener.Addr().String(), http: &http.Client{Transport: &http.Transport{}}}
	defer c.http.CloseIdleConnections()
	c.call(http.MethodPost, "/api/v1/session", protocol.SessionRequest{ClientID: "sandbox-web-test", Scope: protocol.ScopeOwner}, &c.session)
	if c.session.Token == "" {
		t.Fatal("missing session token")
	}
	var board protocol.IssuePage
	c.call(http.MethodGet, "/api/v1/issues?board=true&include_closed=true&limit=100", nil, &board)
	for _, column := range protocol.IssueWorkStatusValues() {
		if board.Counts[column] == 0 {
			t.Fatalf("empty board column %s: %+v", column, board.Counts)
		}
	}
	var inbox protocol.AttentionPage
	c.call(http.MethodGet, "/api/v1/inbox", nil, &inbox)
	kinds := map[string]bool{}
	for _, attention := range inbox.Items {
		kinds[attention.Kind] = true
	}
	for _, kind := range []string{"decision", "action", "information"} {
		if !kinds[kind] {
			t.Fatalf("missing inbox kind %s", kind)
		}
	}
	replay, err := SeedDemo(ctx, r, root)
	if err != nil || fmt.Sprint(replay) != fmt.Sprint(demo) {
		t.Fatalf("seed replay: %+v %v", replay, err)
	}
	var after protocol.IssuePage
	c.call(http.MethodGet, "/api/v1/issues?board=true&include_closed=true&limit=100", nil, &after)
	if len(after.Items) != len(board.Items) {
		t.Fatal("reseed duplicated Issues")
	}
	var allBefore protocol.IssuePage
	c.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &allBefore)
	c.operation("test-create", "conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "创建 一个示例 Issue"})
	waitFor(t, ctx, "main creates an Issue without recursively replaying its source", func() bool {
		var page protocol.IssuePage
		c.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &page)
		for _, issue := range page.Items {
			if issue.Title == "创建 一个示例 Issue" {
				return true
			}
		}
		return false
	})
	c.operation("test-dispatch", "conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "派发 一个示例任务"})
	var dispatched, dispatchedIssue string
	waitFor(t, ctx, "main conversation dispatch creates a task", func() bool {
		if dispatchedIssue == "" {
			var page protocol.IssuePage
			c.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &page)
			for _, item := range page.Items {
				if item.Title == "派发 一个示例任务" {
					dispatchedIssue = item.IssueID
					break
				}
			}
		}
		if dispatchedIssue != "" {
			var issue protocol.IssueView
			c.call(http.MethodGet, "/api/v1/issues/"+dispatchedIssue, nil, &issue)
			if len(issue.Tasks) > 0 {
				dispatched = issue.Tasks[0].TaskID
				return true
			}
		}
		return false
	})
	waitFor(t, ctx, "fake runner finishes dispatched task", func() bool {
		var task protocol.TaskDetail
		c.call(http.MethodGet, "/api/v1/tasks/"+dispatched, nil, &task)
		return task.Lifecycle == protocol.TaskCompleted
	})
	c.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &after)
	if len(after.Items) != len(allBefore.Items)+2 {
		t.Fatalf("creation source recursively produced Issues: before=%d after=%d", len(allBefore.Items), len(after.Items))
	}
	var decision protocol.AttentionView
	c.call(http.MethodGet, "/api/v1/attentions/"+demo.Decision, nil, &decision)
	c.operation("test-decision", "attentions.respond", protocol.Target{Type: "attention", ID: demo.Decision}, &decision.Revision, protocol.RespondAttentionPayload{Version: decision.Decision.Version, Option: "手机优先"})
	c.call(http.MethodGet, "/api/v1/attentions/"+demo.Decision, nil, &decision)
	if decision.Status != "resolved" {
		t.Fatalf("decision not resolved: %+v", decision)
	}
	// The seed's waiting worker also uses the real decision path and resumes.
	waitFor(t, ctx, "worker question appears", func() bool {
		c.call(http.MethodGet, "/api/v1/inbox", nil, &inbox)
		for _, item := range inbox.Items {
			if item.TaskID == demo.Tasks[2] && item.Kind == "decision" && item.Decision != nil {
				c.operation("test-worker-answer", "attentions.respond", protocol.Target{Type: "attention", ID: item.AttentionID}, &item.Revision, protocol.RespondAttentionPayload{Version: item.Decision.Version, Option: "手机"})
				return true
			}
		}
		return false
	})
	waitFor(t, ctx, "answered worker finishes", func() bool {
		var task protocol.TaskDetail
		c.call(http.MethodGet, "/api/v1/tasks/"+demo.Tasks[2], nil, &task)
		return task.Lifecycle == protocol.TaskCompleted
	})
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) { l.t.Log(string(p)); return len(p), nil }

func TestDataProtectionAndReset(t *testing.T) {
	if _, _, err := prepareData("", true); err == nil {
		t.Fatal("missing data admitted")
	}
	root := t.TempDir()
	original := filepath.Join(root, "production.db")
	if err := os.WriteFile(original, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareData(root, true); err == nil {
		t.Fatal("unmarked nonempty directory admitted")
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != "preserve" {
		t.Fatal("unmarked data was changed")
	}
	empty := t.TempDir()
	_, release, err := prepareData(empty, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareData(empty, true); err == nil {
		t.Fatal("running sandbox reset admitted")
	}
	release()
	if err := os.WriteFile(filepath.Join(empty, "old.db"), []byte("demo"), 0600); err != nil {
		t.Fatal(err)
	}
	_, release, err = prepareData(empty, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(filepath.Join(empty, "old.db")); !os.IsNotExist(err) {
		t.Fatal("reset kept old data")
	}
	if err := os.Symlink(root, filepath.Join(empty, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := prepareData(empty, true); err == nil {
		t.Fatal("symlink data admitted")
	}
}

func TestScriptHistoryRetainsNativeReceiptAcrossRestart(t *testing.T) {
	h := testkit.NewHarness()
	h.ThreadPrefix = "isolated-"
	h.Turn = func(thread *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		thread.Say(turn, "reply", "persisted reply")
		return "completed"
	}
	session, err := h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := thread.StartTurn(t.Context(), harness.TurnInput{UserMessageID: "original-source"})
	if err != nil {
		t.Fatal(err)
	}
	for event := range thread.Events() {
		if event.Kind == harness.EventTurnCompleted {
			break
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	h.Wait()
	raw, err := h.ExportHistory()
	if err != nil {
		t.Fatal(err)
	}
	restored := testkit.NewHarness()
	if err := restored.RestoreHistory(raw); err != nil {
		t.Fatal(err)
	}
	next, err := restored.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := next.ResumeThread(t.Context(), thread.ID(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	receipt, found, err := resumed.(harness.TurnReader).FindTurn(t.Context(), "original-source", turn)
	if err != nil || !found || receipt.Status != "completed" || len(receipt.Messages) != 1 || receipt.Messages[0].Text != "persisted reply" {
		t.Fatalf("restored native receipt: %+v %v", receipt, err)
	}
	fresh, err := next.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil || fresh.ID() == thread.ID() {
		t.Fatal("new thread reused an original native identity", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	restored.Wait()
}
