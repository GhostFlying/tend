package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/gorilla/websocket"
)

func TestFault_MaintenanceScheduledAuditCreationAndReuse(t *testing.T) {
	for _, invalidation := range []string{"missing", "instructions"} {
		t.Run(invalidation, func(t *testing.T) {
			testScheduledAuditCreationAndReuse(t, invalidation)
		})
	}
}

func testScheduledAuditCreationAndReuse(t *testing.T, invalidation string) {
	ctx := t.Context()
	initial, _, cfg, opts := fixture(t)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	initial.lock = nil
	native := &auditNativeFixture{threads: make(map[string][]NativeTurn)}
	cfg.Backend.Socket = testkit.SocketPath(t, "native.sock")
	listenAuditFixture(t, cfg.Backend.Socket, http.HandlerFunc(native.serve))
	// Start with a clean store so the first backend is pinned to the fixture.
	cfg.StateDir = t.TempDir()
	if err := os.Chmod(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	var ids atomic.Int64
	var now atomic.Int64
	now.Store(1000)
	opts.ID = func() string { return fmt.Sprintf("id-%d", ids.Add(1)) }
	opts.Now = func() time.Time { return time.Unix(now.Load(), 0) }
	open := func() *Controller {
		opts.Native = &NativeClient{}
		c, err := Open(ctx, cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := open()
	defer func() { _ = c.Close() }()
	var current atomic.Pointer[Controller]
	current.Store(c)
	listenAuditFixture(t, cfg.ControlSocket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().Handler(nil).ServeHTTP(w, r)
	}))
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.ControlSocket)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	request := func(method, path string, input any, dst any) int {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		call, err := http.NewRequestWithContext(ctx, method, "http://localhost/maintenance/"+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(call)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if dst != nil {
			if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	restart := func() {
		t.Helper()
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		c = open()
		current.Store(c)
	}
	runTimer := func() {
		t.Helper()
		if err := Audit(ctx, cfg, opts); err != nil {
			t.Fatal(err)
		}
	}

	runTimer()
	firstRequest := c.Status().PendingAudits[0]
	if err := c.Step(ctx); err == nil {
		t.Fatal("fixture did not lose the original creation response")
	}
	original := c.Status().Active
	if original.Phase != "creating" || original.CreationError == "" || original.SessionID != "" || original.InputID != "" {
		t.Fatalf("missing original creation intent or diagnosis: %+v", original)
	}
	restart()
	for range 2 {
		if err := c.Step(ctx); err == nil {
			t.Fatal("unresolved creation was not held")
		}
	}
	if creates, starts := native.counts(); creates != 1 || starts != 0 {
		t.Fatalf("unknown creation triggered more work: %d creates, %d turns", creates, starts)
	}
	s := c.Status()
	if s.Active.CreationError != original.CreationError || !strings.Contains(s.NativeError, original.CreationError) {
		t.Fatal("waiting replaced the first native diagnosis")
	}

	exit := protocol.MaintenanceRequest{
		RequestID: "settle-original", ExpectedRevision: s.Revision, CreationID: original.ID,
		SessionID: "fixture-thread-1", Reason: "matched the first thread/start allocation in the provider trace",
		ExitEvidence: "provider fixture trace: fixture-thread-1 agent loop exited",
		Verification: "provider fixture trace: fixture-thread-1 listener teardown had_active_turn=false; no turn/start was submitted",
	}
	for _, invalid := range []protocol.MaintenanceRequest{
		{RequestID: exit.RequestID, ExpectedRevision: s.Revision, CreationID: original.ID, SessionID: exit.SessionID, Reason: "socket timeout"},
		{RequestID: exit.RequestID, ExpectedRevision: s.Revision, CreationID: original.ID, SessionID: exit.SessionID, Reason: "no rollout", ExitEvidence: exit.ExitEvidence},
		{RequestID: exit.RequestID, ExpectedRevision: s.Revision - 1, CreationID: original.ID, SessionID: exit.SessionID, Reason: exit.Reason, ExitEvidence: exit.ExitEvidence, Verification: exit.Verification},
		{RequestID: exit.RequestID, ExpectedRevision: s.Revision, CreationID: "different-creation", SessionID: exit.SessionID, Reason: exit.Reason, ExitEvidence: exit.ExitEvidence, Verification: exit.Verification},
	} {
		if code := request(http.MethodPost, "reconcile", invalid, nil); code != http.StatusConflict {
			t.Fatalf("invalid exit reconciliation returned HTTP %d", code)
		}
	}
	if after := c.Status(); after.Revision != s.Revision || after.Active.ID != original.ID {
		t.Fatal("a rejected reconcile changed the original intent")
	}
	var settlement protocol.MaintenanceReceipt
	if code := request(http.MethodPost, "reconcile", exit, &settlement); code != http.StatusOK {
		t.Fatalf("verified exit reconciliation returned HTTP %d", code)
	}
	restart()
	var replay, durable, timer protocol.MaintenanceReceipt
	if request(http.MethodPost, "reconcile", exit, &replay) != http.StatusOK || replay != settlement ||
		request(http.MethodGet, "request/"+exit.RequestID, nil, &durable) != http.StatusOK || durable.Request == nil || *durable.Request != exit ||
		request(http.MethodGet, "request/"+firstRequest, nil, &timer) != http.StatusOK || timer.Request == nil || !timer.Request.ScheduledAudit {
		t.Fatalf("reconcile or timer receipts lost original identity: %+v, %+v, %+v", replay, durable, timer)
	}

	c.recordChanges([]protocol.MaintenanceChange{{IssueID: "changed", State: "active", Revision: 1}})
	step(t, c)
	step(t, c)
	first := c.Status().Active
	if first.ScheduledAuditID != firstRequest || first.Audit.Through <= first.Audit.Since || first.SessionID == exit.SessionID {
		t.Fatalf("original pending audit did not run against the recovered controller: %+v", first)
	}
	native.complete(first.SessionID)
	step(t, c)
	if idle := c.Status(); idle.Active != nil || idle.IdleAuditSession.SessionID != first.SessionID || idle.AuditedThrough != first.Audit.Through {
		t.Fatalf("first audit did not leave an idle reusable thread: %+v", idle)
	}
	restart()
	step(t, c)
	runTimer() // Same occurrence, even across restart.
	step(t, c)
	if creates, starts := native.counts(); creates != 2 || starts != 1 {
		t.Fatalf("idle or duplicate timer launched work: %d creates, %d turns", creates, starts)
	}

	now.Add(int64((2 * time.Hour) / time.Second))
	var wg sync.WaitGroup
	dispatches := make(chan error, 4)
	for range cap(dispatches) {
		wg.Go(func() { dispatches <- Audit(ctx, cfg, opts) })
	}
	wg.Wait()
	close(dispatches)
	for err := range dispatches {
		if err != nil {
			t.Fatal(err)
		}
	}
	if pending := c.Status().PendingAudits; len(pending) != 1 {
		t.Fatalf("concurrent scheduling did not coalesce to one audit: %v", pending)
	}
	secondRequest := c.Status().PendingAudits[0]
	native.loseNextTurnReply()
	if err := c.Step(ctx); err == nil {
		t.Fatal("fixture did not lose the second audit's turn response")
	}
	second := c.Status().Active
	if second == nil || second.SessionID != first.SessionID || second.ScheduledAuditID != secondRequest ||
		second.InputID == first.InputID || second.TurnID != "" || second.Audit.Since != first.Audit.Through ||
		second.Audit.Through != second.Audit.Since || !strings.Contains(second.Prompt, "Independently look for missed maintenance work") {
		t.Fatalf("reused audit did not preserve a distinct frozen input with an empty cursor range: %+v", second)
	}
	restart()
	remembered := native.remove(second.SessionID)
	if err := c.Step(ctx); err == nil || c.Status().Active.InputID != second.InputID {
		t.Fatal("missing native history settled an accepted but unconfirmed input")
	}
	native.restore(second.SessionID, remembered)
	for range 4 {
		wg.Go(func() {
			if err := c.Step(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if creates, starts := native.counts(); creates != 2 || starts != 2 {
		t.Fatalf("lost receipt replayed the second audit: %d creates, %d turns", creates, starts)
	}
	native.complete(second.SessionID)
	step(t, c)
	if c.Status().Active != nil {
		t.Fatal("recovered turn did not complete")
	}

	now.Add(int64((2 * time.Hour) / time.Second))
	runTimer()
	if invalidation == "missing" {
		native.remove(first.SessionID)
	} else if err := os.WriteFile(cfg.InstructionsFile, []byte("Updated scope."), 0600); err != nil {
		t.Fatal(err)
	}
	step(t, c)
	step(t, c)
	third := c.Status().Active
	if third.SessionID == first.SessionID || third.Audit.Since != first.Audit.Through {
		t.Fatalf("replacement did not use the durable audit cursor: %+v", third)
	}
	native.complete(third.SessionID)
	step(t, c)
	if creates, starts := native.counts(); creates != 3 || starts != 3 {
		t.Fatalf("want three actual audits including the replacement: %d creates, %d turns", creates, starts)
	}
	var history struct {
		Items []protocol.MaintenanceSession `json:"items"`
	}
	if request(http.MethodGet, "history", nil, &history) != http.StatusOK || len(history.Items) != 4 {
		t.Fatalf("unexpected history: %+v", history)
	}
	old, a, b := history.Items[0], history.Items[1], history.Items[2]
	if old.ID != original.ID || old.CreationError != original.CreationError || old.ReconciledBy != exit.RequestID ||
		old.ObservedSessionID != exit.SessionID || old.Phase != "creation-exited" || a.ID == b.ID ||
		a.ScheduledAuditID != firstRequest || b.ScheduledAuditID != secondRequest || a.SessionID != b.SessionID ||
		a.TurnID == "" || b.TurnID == "" || a.TurnID == b.TurnID || a.Outcome != "completed" || b.Outcome != "completed" ||
		a.Audit.Through != b.Audit.Since || b.Audit.Since != b.Audit.Through {
		t.Fatalf("history lost creation evidence or individual audit receipts: %+v", history)
	}
}

func listenAuditFixture(t *testing.T, socket string, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
}

type auditNativeFixture struct {
	mu              sync.Mutex
	threads         map[string][]NativeTurn
	creates, starts int
	loseTurnReply   bool
}

func (f *auditNativeFixture) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.starts
}
func (f *auditNativeFixture) complete(thread string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	turns := f.threads[thread]
	turns[len(turns)-1].Status = "completed"
}
func (f *auditNativeFixture) remove(thread string) []NativeTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	turns := f.threads[thread]
	delete(f.threads, thread)
	return turns
}
func (f *auditNativeFixture) restore(thread string, turns []NativeTurn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads[thread] = turns
}
func (f *auditNativeFixture) loseNextTurnReply() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loseTurnReply = true
}

func (f *auditNativeFixture) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
			Params struct {
				ThreadID     string `json:"threadId"`
				InputID      string `json:"clientUserMessageId"`
				IncludeTurns bool   `json:"includeTurns"`
			} `json:"params"`
		}
		if conn.ReadJSON(&req) != nil {
			return
		}
		if req.ID == nil {
			continue
		}
		var turns []map[string]any
		f.mu.Lock()
		state, exists := f.threads[req.Params.ThreadID]
		status := "idle"
		var result any = map[string]any{}
		var rejection any
		drop := false
		switch req.Method {
		case "thread/start":
			f.creates++
			id := fmt.Sprintf("fixture-thread-%d", f.creates)
			if f.creates == 1 {
				// The provider allocates this thread, then exits without
				// materializing it or returning a creation response.
				drop = true
			} else {
				f.threads[id] = nil
				result = map[string]any{"thread": map[string]any{"id": id}}
			}
		case "thread/read":
			if !exists {
				rejection = map[string]any{"code": -32600, "message": "thread not loaded: " + req.Params.ThreadID}
				break
			}
			if slices.ContainsFunc(state, func(t NativeTurn) bool { return t.Status == "inProgress" }) {
				status = "active"
			}
			if req.Params.IncludeTurns {
				for _, v := range state {
					turns = append(turns, map[string]any{"id": v.ID, "status": v.Status, "items": []any{map[string]string{"type": "userMessage", "clientId": v.InputID}}})
				}
			}
			result = map[string]any{"thread": map[string]any{"status": map[string]string{"type": status}, "turns": turns}}
		case "turn/start":
			f.starts++
			turn := NativeTurn{ID: fmt.Sprintf("fixture-turn-%d", f.starts), InputID: req.Params.InputID, Status: "inProgress"}
			f.threads[req.Params.ThreadID] = append(state, turn)
			result = map[string]any{"turn": map[string]any{"id": turn.ID}}
			drop = f.loseTurnReply
			f.loseTurnReply = false
		}
		f.mu.Unlock()
		if drop || conn.WriteJSON(map[string]any{"id": req.ID, "result": result, "error": rejection}) != nil {
			return
		}
	}
}
