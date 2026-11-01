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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

type webClient struct {
	t       *testing.T
	ctx     context.Context
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
	// Functional reads share the test budget, not a latency SLA under race instrumentation.
	ctx, cancel := context.WithCancel(c.ctx)
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
	// Race-instrumented SQLite can be slow under shared-host load.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ready := make(chan Demo, 1)
	runtime := make(chan *serverkit.Runtime, 1)
	done := make(chan error, 1)
	var interrupting atomic.Bool
	go func() {
		done <- Run(ctx, Options{FastPacing: true, skipStory: true, Data: root, Listener: listener, replyDelta: func(ctx context.Context, _ harness.TurnInput) {
			if interrupting.Load() {
				<-ctx.Done()
			}
		}, Ready: func(r *serverkit.Runtime, demo Demo) { runtime <- r; ready <- demo }}, slog.New(slog.NewTextHandler(testLog{t}, nil)))
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
	c := webClient{t: t, ctx: ctx, url: "http://" + listener.Addr().String(), http: &http.Client{Transport: &http.Transport{}}}
	defer c.http.CloseIdleConnections()
	c.call(http.MethodPost, "/api/v1/session", protocol.SessionRequest{ClientID: "sandbox-web-test", Scope: protocol.ScopeOwner}, &c.session)
	if c.session.Token == "" {
		t.Fatal("missing session token")
	}
	var board protocol.IssuePage
	c.call(http.MethodGet, "/api/v1/issues?board=true&include_closed=true&limit=100", nil, &board)
	ownerAssigned := 0
	for _, id := range demo.Issues {
		var view protocol.IssueView
		c.call(http.MethodGet, "/api/v1/issues/"+id, nil, &view)
		if view.Assignee.Kind == protocol.IssueAssigneeOwner {
			ownerAssigned++
		} else if view.Assignee.Ref != "sandbox-hosted/manager" {
			t.Fatal("unexpected board assignee", view.Assignee)
		}
	}
	if ownerAssigned != len(demo.Issues) {
		t.Fatalf("legacy board owner examples=%d, want %d", ownerAssigned, len(demo.Issues))
	}
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
	seedBodies := map[string]bool{}
	for _, attention := range inbox.Items {
		if attention.IssueID != "" || attention.TaskID != "" {
			continue
		}
		var body string
		if err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT body FROM conversation_messages WHERE message_id=?`, attention.SourceMessageID).Scan(&body)
		}); err != nil {
			t.Fatal(err)
		}
		if body == attention.Title || len(body) < 40 || seedBodies[body] {
			t.Fatalf("seed attention lacks a distinct explanatory source: %+v %q", attention, body)
		}
		seedBodies[body] = true
	}
	if len(seedBodies) != 3 {
		t.Fatalf("seed attention descriptions: got %d, want 3", len(seedBodies))
	}
	if err := os.Remove(filepath.Join(root, "demo.json")); err != nil {
		t.Fatal(err)
	}
	replay, err := seedDemo(ctx, r, root, true, false)
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
	var dispatchSource string
	var dispatchSeq int64
	if err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT message_id,seq FROM conversation_messages WHERE conversation_id='main' AND author='owner' AND body=?`, "派发 一个示例任务").Scan(&dispatchSource, &dispatchSeq)
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, "dispatch source and completed reply are classified and committed", func() bool {
		var page protocol.ConversationPage
		c.call(http.MethodGet, fmt.Sprintf("/api/v1/conversation?after=%d&limit=200", dispatchSeq-1), nil, &page)
		sourceDone, replyDone, committed := false, false, false
		for _, item := range page.Items {
			if item.MessageID != dispatchSource && item.ReplyTo != dispatchSource {
				continue
			}
			if item.Tool != nil {
				if item.Tool.Status == protocol.ToolFailed || bytes.Contains([]byte(item.Tool.Output), []byte(`"ok":false`)) {
					t.Fatalf("dispatch tool failed: %+v", item.Tool)
				}
				if item.Tool.Name == "conversation_turn_commit" && item.Tool.Status == protocol.ToolCompleted {
					committed = true
				}
				continue
			}
			var associations protocol.MessageAssociations
			err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
				var err error
				associations, err = store.MessageAssociations(ctx, tx.Q(), item.MessageID)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			linked := associations.Disposition == "linked" && len(associations.Items) == 1 && associations.Items[0].WorkItemID == dispatchedIssue
			reviewed := associations.FactsReviewed && associations.Processing != nil && associations.Processing.Status == "completed"
			if item.MessageID == dispatchSource {
				// Owner metadata may point at an active manager turn sharing this
				// source. The completed assistant below witnesses the main commit.
				sourceDone = linked && associations.FactsReviewed && item.State == protocol.MessageReplied
			} else if item.Author == "main_agent" {
				replyDone = linked && reviewed && item.AssistantState == protocol.AssistantCompleted
			}
		}
		return sourceDone && replyDone && committed
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
	// Read only new items so historical association projection cannot consume
	// the short streaming window before the interruption request is sent.
	var interruptAfter int64
	if err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM conversation_messages`).Scan(&interruptAfter)
	}); err != nil {
		t.Fatal(err)
	}
	interruptPath := fmt.Sprintf("/api/v1/conversation?after=%d&limit=200", interruptAfter)
	// Interrupt through the real owner API while the sandbox script is streaming.
	interrupting.Store(true)
	c.operation("test-interrupt-send", "conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "请介绍沙盒的交互能力"})
	var interruptedSource, interruptedReply string
	for interruptedReply == "" {
		var page protocol.ConversationPage
		c.call(http.MethodGet, interruptPath, nil, &page)
		for _, item := range page.Items {
			if item.Author == "owner" && item.Body == "请介绍沙盒的交互能力" {
				interruptedSource = item.MessageID
			}
			if interruptedSource != "" && item.ReplyTo == interruptedSource && item.AssistantState == protocol.AssistantStreaming {
				interruptedReply = item.MessageID
			}
		}
		if interruptedReply == "" && !delay(ctx, 10*time.Millisecond) {
			t.Fatal("did not observe streaming before interruption")
		}
	}
	c.operation("test-interrupt", "conversation.interrupt_reply", protocol.Target{Type: "message", ID: interruptedSource}, nil, protocol.InterruptReplyPayload{MessageID: interruptedSource})
	var stopped protocol.ConversationPage
	waitFor(t, ctx, "owner and assistant interruption are persisted", func() bool {
		c.call(http.MethodGet, interruptPath, nil, &stopped)
		ownerStopped, replyStopped := false, false
		for _, item := range stopped.Items {
			ownerStopped = ownerStopped || item.MessageID == interruptedSource && item.State == protocol.MessageInterrupted
			replyStopped = replyStopped || item.MessageID == interruptedReply && item.AssistantState == protocol.AssistantInterrupted
		}
		return ownerStopped && replyStopped
	})
	turnItems := func(page protocol.ConversationPage) string {
		var items []protocol.ConversationContent
		for _, item := range page.Items {
			if item.ReplyTo == interruptedSource {
				if item.AssistantState == protocol.AssistantCompleted {
					t.Fatal("interrupted reply later completed")
				}
				items = append(items, item.ConversationContent)
			}
		}
		raw, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	beforeStop := turnItems(stopped)
	if !delay(ctx, 700*time.Millisecond) {
		t.Fatal(ctx.Err())
	}
	var later protocol.ConversationPage
	c.call(http.MethodGet, interruptPath, nil, &later)
	if turnItems(later) != beforeStop {
		t.Fatal("script emitted text or tool callbacks after interrupted completion")
	}
	interrupting.Store(false)
	c.operation("test-after-interrupt", "conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "中断后继续对话"})
	waitFor(t, ctx, "conversation continues after interruption", func() bool {
		c.call(http.MethodGet, interruptPath, nil, &later)
		for _, item := range later.Items {
			if item.Author == "owner" && item.Body == "中断后继续对话" && item.State == protocol.MessageReplied {
				return true
			}
		}
		return false
	})
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) { l.t.Log(string(p)); return len(p), nil }

func TestDataProtectionAndReset(t *testing.T) {
	if _, _, err := prepareData("", true); err == nil {
		t.Fatal("missing data admitted")
	}
	root := t.TempDir()
	linked := filepath.Join(t.TempDir(), "linked-data")
	if err := os.Symlink(root, linked); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := prepareData(linked, false); err == nil {
		t.Fatal("explicit symlinked data directory admitted")
	}
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

func TestScriptInterruptionJoinsBeforeCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := testkit.NewHarness()
	sessions := &scriptedSessions{harness: h, ctx: ctx}
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.Turn = func(th *testkit.FakeThread, turn string, input harness.TurnInput) string {
		return sessions.play(th, turn, input, func(ctx context.Context, _ *testkit.FakeThread, _ string, _ harness.TurnInput) string {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return "completed"
		})
	}
	session, err := sessions.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		close(release)
		_ = session.Close()
		h.Wait()
	}()
	thread, err := session.StartThread(ctx, harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := thread.StartTurn(ctx, harness.TurnInput{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	interrupted := make(chan error, 1)
	go func() { interrupted <- thread.Interrupt(ctx, turn) }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("interrupt did not cancel the script")
	}
	select {
	case err := <-interrupted:
		t.Fatalf("interrupt returned before script exited: %v", err)
	default:
	}
	// Let the script exit without closing the channel twice in cleanup.
	release <- struct{}{}
	select {
	case err := <-interrupted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("interrupt did not join the script")
	}
	for {
		select {
		case event := <-thread.Events():
			if event.Kind == harness.EventTurnCompleted {
				if event.TurnStatus != "interrupted" {
					t.Fatalf("script completed after cancellation: %+v", event)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("missing interrupted completion")
		}
	}
}

func TestSandboxRetainedDataRestartReconcilesRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	root := t.TempDir()
	var heartbeatReplies atomic.Int64
	type started struct {
		runtime *serverkit.Runtime
		demo    Demo
	}
	start := func() (*serverkit.Runtime, Demo, *webClient, func()) {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		runCtx, stopRun := context.WithCancel(ctx)
		ready, done := make(chan started, 1), make(chan error, 1)
		transport := &http.Transport{}
		t.Cleanup(transport.CloseIdleConnections)
		go func() {
			done <- Run(runCtx, Options{FastPacing: true, skipStory: true, Data: root, Listener: listener, runnerTransport: heartbeatObserver{transport, &heartbeatReplies}, Ready: func(r *serverkit.Runtime, d Demo) { ready <- started{r, d} }}, slog.New(slog.NewTextHandler(testLog{t}, nil)))
		}()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				stopRun()
				select {
				case err := <-done:
					if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
						t.Error(err)
					}
				case <-time.After(10 * time.Second):
					t.Error("restarted sandbox did not stop")
				}
			})
		}
		t.Cleanup(stop)
		var current started
		select {
		case current = <-ready:
		case err := <-done:
			done <- err
			t.Fatalf("sandbox startup: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		client := &webClient{t: t, ctx: ctx, url: "http://" + listener.Addr().String(), http: &http.Client{Transport: &http.Transport{}}}
		t.Cleanup(client.http.CloseIdleConnections)
		client.call(http.MethodPost, "/api/v1/session", protocol.SessionRequest{ClientID: "sandbox-restart-test", Scope: protocol.ScopeOwner}, &client.session)
		return current.runtime, current.demo, client, stop
	}
	dispatch := func(c *webClient, key, title string) string {
		t.Helper()
		c.operation(key, "conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: title})
		var taskID string
		waitFor(t, ctx, "restart dispatch reaches a completed task", func() bool {
			var page protocol.IssuePage
			c.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &page)
			for _, item := range page.Items {
				if item.Title != title {
					continue
				}
				var issue protocol.IssueView
				c.call(http.MethodGet, "/api/v1/issues/"+item.IssueID, nil, &issue)
				if len(issue.Tasks) > 0 {
					taskID = issue.Tasks[0].TaskID
					var task protocol.TaskDetail
					c.call(http.MethodGet, "/api/v1/tasks/"+taskID, nil, &task)
					return task.Lifecycle == protocol.TaskCompleted
				}
			}
			return false
		})
		return taskID
	}
	first, demo, client, stop := start()
	completed := dispatch(client, "restart-before", "派发 重启前的示例任务")
	var before protocol.IssuePage
	client.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &before)
	var pending protocol.AttentionView
	waitFor(t, ctx, "a real worker question remains in flight at shutdown", func() bool {
		var inbox protocol.AttentionPage
		client.call(http.MethodGet, "/api/v1/inbox", nil, &inbox)
		for _, item := range inbox.Items {
			if item.TaskID == demo.Tasks[2] && item.Kind == "decision" && item.Decision != nil {
				pending = item
				return true
			}
		}
		return false
	})
	var previous store.HostRuntimeRow
	if err := first.Store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		previous, err = store.GetHostRuntime(ctx, tx.Q(), hostID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stop()
	heartbeatReplies.Store(0)
	second, retained, client, stopAgain := start()
	defer stopAgain()
	if fmt.Sprint(retained) != fmt.Sprint(demo) {
		t.Fatal("restart duplicated or replaced demo identities")
	}
	var current store.HostRuntimeRow
	if err := second.Store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		current, err = store.GetHostRuntime(ctx, tx.Q(), hostID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if current.Runtime.InstanceID == previous.Runtime.InstanceID || current.Runtime.Generation <= previous.Runtime.Generation {
		t.Fatal("readiness accepted the previous runner incarnation")
	}
	waitFor(t, ctx, "the restarted runner has a successful real heartbeat", func() bool {
		return heartbeatReplies.Load() > 0
	})
	var after protocol.IssuePage
	client.call(http.MethodGet, "/api/v1/issues?include_closed=true&limit=100", nil, &after)
	if len(before.Items) != len(after.Items) {
		t.Fatal("restart changed the persisted Issue set")
	}
	var task protocol.TaskDetail
	client.call(http.MethodGet, "/api/v1/tasks/"+completed, nil, &task)
	if task.Lifecycle != protocol.TaskCompleted {
		t.Fatal("completed task did not survive restart")
	}
	client.call(http.MethodGet, "/api/v1/tasks/"+demo.Tasks[2], nil, &task)
	if task.Lifecycle == protocol.TaskCompleted {
		t.Fatal("restart fabricated success for the unanswered in-flight run")
	}
	client.call(http.MethodGet, "/api/v1/attentions/"+pending.AttentionID, nil, &pending)
	if pending.Status == "open" {
		client.operation("restart-answer", "attentions.respond", protocol.Target{Type: "attention", ID: pending.AttentionID}, &pending.Revision, protocol.RespondAttentionPayload{Version: pending.Decision.Version, Option: "手机"})
	}
	dispatch(client, "restart-after", "派发 重启后的示例任务")
}

// heartbeatObserver records the real runner's acknowledged response; it never
// creates requests or substitutes a server result.
type heartbeatObserver struct {
	transport  http.RoundTripper
	successful *atomic.Int64
}

func (o heartbeatObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	var env protocol.Envelope
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		err = json.NewDecoder(body).Decode(&env)
		_ = body.Close()
		if err != nil {
			return nil, err
		}
	}
	response, err := o.transport.RoundTrip(request)
	if err != nil || env.Operation != protocol.OpRunnerHeartbeat {
		return response, err
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var result struct {
		Operation *protocol.Operation `json:"operation"`
		Error     *protocol.Error     `json:"error"`
	}
	if json.Unmarshal(body, &result) == nil && result.Error == nil && result.Operation != nil && result.Operation.Status == protocol.OpSucceeded {
		o.successful.Add(1)
	}
	return response, nil
}

func TestSandboxPacingProfiles(t *testing.T) {
	live := pacing(false)
	turn := live.thinking + 2*live.tool + live.reply
	if turn < 6*time.Second || turn > 12*time.Second {
		t.Fatalf("turn pacing outside observable range: %v", turn)
	}
	if run := 4 * live.workerStep; run < 15*time.Second || run > 25*time.Second {
		t.Fatalf("run pacing outside observable range: %v", run)
	}
	fast := pacing(true)
	if fast.thinking+2*fast.tool+fast.reply+4*fast.workerStep > time.Second {
		t.Fatal("fast scripts should not materially delay tests")
	}
}

func TestSandboxWorkerLiveSequence(t *testing.T) {
	workers := &scriptedWorkers{pacing: pacing(true)}
	worker, err := workers.Start(t.Context(), harness.WorkerSpec{RunID: "run-sequence", Role: protocol.RoleAccept})
	if err != nil {
		t.Fatal(err)
	}
	var kinds, types, items []string
	for event := range worker.Events() {
		var body struct {
			Item string `json:"item_id"`
			Data struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, event.Kind)
		types = append(types, body.Data.Type)
		items = append(items, body.Item)
	}
	if _, err := worker.Wait(); err != nil {
		t.Fatal(err)
	}
	want := []string{"reasoning", "mcpToolCall", "commandExecution", "agentMessage"}
	if len(kinds) != 2*len(want)+2 || kinds[0] != "turn.started" || kinds[len(kinds)-1] != "turn.completed" {
		t.Fatalf("expected paired lifecycle events, got %v", kinds)
	}
	kinds, types, items = kinds[1:len(kinds)-1], types[1:len(types)-1], items[1:len(items)-1]
	for n, kind := range want {
		if kinds[2*n] != "item.started" || kinds[2*n+1] != "item.completed" || types[2*n] != kind || types[2*n+1] != kind || items[2*n] != items[2*n+1] {
			t.Fatalf("invalid stage %d: %v %v %v", n, kinds, types, items)
		}
	}
}

func TestSandboxWorkerPacingCancellation(t *testing.T) {
	workers := &scriptedWorkers{pacing: scriptPacing{workerStep: time.Hour}}
	worker, err := workers.Start(t.Context(), harness.WorkerSpec{RunID: "run-cancel", Role: protocol.RoleAccept})
	if err != nil {
		t.Fatal(err)
	}
	<-worker.Events()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := workers.Release(ctx, "run-cancel", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Wait(); err != context.Canceled {
		t.Fatalf("expected canceled worker, got %v", err)
	}
}

func TestSandboxStory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ready := make(chan struct {
		runtime *serverkit.Runtime
		demo    Demo
	}, 1)
	done := make(chan error, 1)
	var first sync.Once
	go func() {
		done <- Run(ctx, Options{FastPacing: true, storyOnly: true, turnInput: func(input harness.TurnInput) harness.TurnInput {
			first.Do(func() { input.Repair = true })
			return input
		}, Data: root, Listener: listener, Ready: func(r *serverkit.Runtime, d Demo) {
			ready <- struct {
				runtime *serverkit.Runtime
				demo    Demo
			}{r, d}
		}}, slog.New(slog.NewTextHandler(testLog{t}, nil)))
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("story did not stop")
		}
	}()
	var r *serverkit.Runtime
	var demo Demo
	select {
	case value := <-ready:
		r, demo = value.runtime, value.demo
	case err := <-done:
		done <- err
		t.Fatalf("story startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The initial source was settled by a metadata-only repair. A recovery
	// source must create the business object before seed advancement.
	var recoveries int
	if err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM request_records WHERE request_id=?`, seedRequest("story-create-recovery-1")).Scan(&recoveries)
	}); err != nil {
		t.Fatal(err)
	}
	if recoveries != 1 {
		t.Fatalf("missing stable recovery request: %d", recoveries)
	}
	if demo.Story == "" || demo.StoryTask == "" || demo.StoryDecision == "" {
		t.Fatal("missing multi-round story receipt", demo)
	}
	if err := r.Store.Read(ctx, func(tx *store.ReadTx) error {
		issue, err := views.CanonicalIssue(ctx, tx.Q(), demo.Story, 0)
		if err != nil {
			return err
		}
		if issue.Plan.Title != storyTitle || issue.WorkStatus != protocol.IssueInReview || issue.Progress == nil || issue.Progress.UpdatedBy.Kind != protocol.IssueAssigneeAgent {
			t.Fatal("story did not reach manager-attributed review", issue)
		}
		before := int64(0)
		blocks, attributed, continuation := 0, false, false
		for {
			page, err := views.IssueActivity(ctx, tx.Q(), demo.Story, before)
			if err != nil {
				return err
			}
			for _, entry := range page.Items {
				if entry.Block != nil {
					blocks++
					continuation = continuation || entry.Block.More
				}
				if entry.Kind == "issue.progress_updated" && entry.Author == "沙盒负责人 Agent" {
					attributed = true
				}
				if entry.Block == nil && entry.Author == "" {
					t.Fatal("unattributed story event", entry)
				}
			}
			if !page.More {
				break
			}
			before = page.NextBefore
		}
		if blocks < 8 || !attributed || !continuation || before == 0 {
			t.Fatalf("story activity: blocks=%d attributed=%v continuation=%v older=%d", blocks, attributed, continuation, before)
		}
		task, err := views.Task(ctx, tx.Q(), demo.StoryTask)
		if err != nil {
			return err
		}
		failed, succeeded, accepted := false, false, false
		for _, run := range task.Runs {
			failed = failed || run.Observed == protocol.RunFailed
			succeeded = succeeded || run.Role == protocol.RoleImplement && run.Generation > 1 && run.Observed == protocol.RunFinished
		}
		for _, acceptance := range task.Acceptances {
			accepted = accepted || acceptance.Verdict == protocol.VerdictPassed
		}
		if !failed || !succeeded || !accepted || len(task.Artifacts) == 0 || len(task.Acceptances) == 0 {
			t.Fatal("missing real failure, retry, report or independent acceptance", task)
		}
		attention, err := views.Attention(ctx, tx.Q(), demo.StoryDecision)
		if err != nil {
			return err
		}
		if attention.Decision == nil || !bytes.Contains(attention.Decision.Answer, []byte("手机布局")) || attention.Decision.Status != "answered" {
			t.Fatal("story decision was not answered", attention)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Existing completion receipts are upgraded through the same stable story
	// commands, without resetting retained demo data or duplicating history.
	legacy := demo
	legacy.Story, legacy.StoryTask, legacy.StoryDecision = "", "", ""
	if err := writeDemoReceipt(filepath.Join(root, "demo.json"), legacy); err != nil {
		t.Fatal(err)
	}
	upgraded, err := SeedDemo(ctx, r, root)
	if err != nil || fmt.Sprint(upgraded) != fmt.Sprint(demo) {
		t.Fatalf("legacy receipt upgrade: %+v %v", upgraded, err)
	}

	if err := os.Remove(filepath.Join(root, "demo.json")); err != nil {
		t.Fatal(err)
	}
	replay, err := seedDemo(ctx, r, root, false, true)
	if err != nil || fmt.Sprint(replay) != fmt.Sprint(demo) {
		t.Fatalf("story replay: %+v %v", replay, err)
	}
}

type webRoundTripper func(*http.Request) (*http.Response, error)

func (f webRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebClientUsesEnclosingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	called := false
	client := webClient{t: t, ctx: ctx, url: "http://example.invalid", http: &http.Client{
		Transport: webRoundTripper(func(r *http.Request) (*http.Response, error) {
			called = true
			if got, ok := r.Context().Deadline(); !ok || !got.Equal(want) {
				t.Fatalf("owner request deadline = %v, want test deadline %v", got, want)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte(`{}`)))}, nil
		}),
	}}
	client.call(http.MethodGet, "/api/v1/conversation", nil, nil)
	if !called {
		t.Fatal("owner request did not reach the transport")
	}
}

func TestSandboxInboxPendingHistoryAndRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan Demo, 1)
	done := make(chan error, 1)
	root := t.TempDir()
	go func() {
		done <- Run(ctx, Options{Scenario: "inbox", FastPacing: true, Data: root, Listener: listener, Ready: func(_ *serverkit.Runtime, demo Demo) { ready <- demo }}, slog.New(slog.NewTextHandler(testLog{t}, nil)))
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("inbox sandbox did not stop")
		}
	}()
	var demo Demo
	select {
	case demo = <-ready:
	case err := <-done:
		done <- err
		t.Fatalf("inbox seed: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	client := webClient{t: t, ctx: ctx, url: "http://" + listener.Addr().String(), http: &http.Client{Transport: &http.Transport{}}}
	defer client.http.CloseIdleConnections()
	client.call(http.MethodPost, "/api/v1/session", protocol.SessionRequest{ClientID: "inbox-test", Scope: protocol.ScopeOwner}, &client.session)
	var pending, all protocol.AttentionPage
	client.call(http.MethodGet, "/api/v1/inbox?status=open&include_archived=true", nil, &pending)
	client.call(http.MethodGet, "/api/v1/inbox?include_archived=true", nil, &all)
	if len(pending.Items) != 1 || len(all.Items) != 3 || pending.Items[0].AttentionID != demo.Decision || pending.Items[0].IssueTitle != "移动端 PWA 安全区、附件间距与输入光标修复" || pending.Items[0].TaskTitle == "" || pending.Items[0].TaskContext == nil || pending.Items[0].TaskContext.HasCurrentArtifact {
		t.Fatalf("inbox context mismatch: pending=%+v all=%+v", pending, all)
	}
	a := pending.Items[0]
	client.operation("inbox-retry", "attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, &a.Revision, protocol.RespondAttentionPayload{Version: a.QuestionVersion, Option: "retry the implementation"})
	client.call(http.MethodGet, "/api/v1/inbox?status=open&include_archived=true", nil, &pending)
	client.call(http.MethodGet, "/api/v1/inbox?include_archived=true", nil, &all)
	if len(pending.Items) != 0 || len(all.Items) != 3 {
		t.Fatal("answer did not move the original item out of pending", pending, all)
	}
	var task protocol.TaskDetail
	client.call(http.MethodGet, "/api/v1/tasks/"+demo.Tasks[0], nil, &task)
	if len(task.Actions) != 2 || task.Actions[1].Generation != 2 || len(task.Runs) != 1 || task.Runs[0].Observed != protocol.RunFailed {
		t.Fatal("retry did not retain failure and enqueue the next explicit generation", task)
	}
	var answered protocol.AttentionView
	client.call(http.MethodGet, "/api/v1/attentions/"+a.AttentionID, nil, &answered)
	if answered.Status != "resolved" || answered.DecisionID != a.DecisionID || answered.TaskContext == nil || answered.TaskContext.HasCurrentArtifact || answered.TaskContext.CurrentArtifactAccepted {
		t.Fatal("retry lost the original question or current delivery evidence", answered)
	}
}
