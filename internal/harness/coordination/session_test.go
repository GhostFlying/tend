package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type permissionSession struct{ *testkit.FakeSession }
type permissionThread struct{ *testkit.FakeThread }

func (t permissionThread) PermissionDefaults() harness.PermissionSettings {
	return harness.PermissionSettings{Sandbox: t.Config.Sandbox, ApprovalPolicy: t.Config.ApprovalPolicy}
}

type retainedMemoryTransport struct {
	*memoryTransport
	replayed atomic.Int64
}

func (t *retainedMemoryTransport) ReplayToolResult(_ context.Context, e Event) (harness.ToolResult, error) {
	if e.SourceRunID != "original-run" {
		return harness.ToolResult{}, errors.New("wrong original run")
	}
	t.replayed.Add(1)
	return harness.ToolResult{Success: true, Text: "ORIGINAL_RESULT"}, nil
}

func TestHistoricalCallbacksUseRetainedResultsAndDoNotDisconnectNewAttachment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fake := testkit.NewHarness()
	native, err := fake.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport := &retainedMemoryTransport{memoryTransport: &memoryTransport{events: make(chan Event, 16)}}
	transport.driver = &Driver{Session: permissionSession{native.(*testkit.FakeSession)}, Profile: protocol.ExecutionProfile{Model: "model"}, Emit: transport.emit}
	if err := transport.emit(ctx, Event{Historical: true, SourceRunID: "original-run", ThreadID: "th_1", Call: &harness.ToolCall{ThreadID: "th_1", TurnID: "original-turn", CallID: "old-call", Tool: "must_not_execute"}}); err != nil {
		t.Fatal(err)
	}
	if err := transport.emit(ctx, Event{Historical: true, SourceRunID: "original-run", ThreadID: "th_1", Disconnected: true}); err != nil {
		t.Fatal(err)
	}
	session := New(ctx, transport)
	defer session.Close()
	thread, err := session.StartThread(ctx, harness.ThreadConfig{Sandbox: "read-only", ApprovalPolicy: "never", OnToolCall: func(context.Context, harness.ToolCall) harness.ToolResult {
		t.Error("historical callback executed a new business tool")
		return harness.ToolResult{}
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-thread.Events():
		if event.Kind != harness.EventCoordinatorHistoryEnd {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	turn, err := thread.StartTurn(ctx, harness.TurnInput{Text: "Current attachment remains live"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-thread.Events():
			if event.Kind == harness.EventTurnCompleted && event.TurnID == turn {
				if transport.replayed.Load() != 1 {
					t.Fatal("original result was not read", transport.replayed.Load())
				}
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func (s permissionSession) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	t, err := s.FakeSession.StartThread(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return permissionThread{t.(*testkit.FakeThread)}, nil
}
func (s permissionSession) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	t, err := s.FakeSession.ResumeThread(ctx, id, cfg)
	if err != nil {
		return nil, err
	}
	return permissionThread{t.(*testkit.FakeThread)}, nil
}

type memoryTransport struct {
	driver *Driver
	events chan Event
	mu     sync.Mutex
	seq    int64
}

func (m *memoryTransport) Call(ctx context.Context, method string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	result, err := m.driver.Handle(ctx, method, raw)
	if err != nil {
		return err
	}
	if output != nil {
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, output)
	}
	return nil
}
func (m *memoryTransport) Events(ctx context.Context, _ int64) ([]Event, error) {
	select {
	case e := <-m.events:
		return []Event{e}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (m *memoryTransport) emit(ctx context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	e.Sequence = m.seq
	select {
	case m.events <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRemoteCoordinatorRunsToolsAndResumesNativeSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fake := testkit.NewHarness()
	fake.Models = []harness.Model{{ID: "manager-model", Name: "manager-model"}}
	called := make(chan struct{}, 2)
	fake.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if th.Config.Cwd != "/runner/private-session" || th.Config.DeveloperInstructions != "Coordinator role\nPRIVATE_RUNNER_INSTRUCTIONS" {
			t.Errorf("native config did not use runner-local settings: %+v", th.Config)
		}
		result := th.CallTool(ctx, turn, "native-call", "ledger_read", map[string]any{"issue": "own"})
		if !result.Success || result.Text != "scoped result" {
			t.Errorf("tool result=%+v", result)
		}
		return "completed"
	}
	var id string
	for i := 0; i < 2; i++ {
		native, err := fake.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		transport := &memoryTransport{events: make(chan Event, 64)}
		transport.driver = &Driver{Session: permissionSession{native.(*testkit.FakeSession)}, Directory: "/runner/private-session", Instructions: "PRIVATE_RUNNER_INSTRUCTIONS", Profile: protocol.ExecutionProfile{Model: "manager-model"}, Emit: transport.emit}
		s := New(ctx, transport)
		cfg := harness.ThreadConfig{Cwd: "/server/path-must-not-be-used", Model: "manager-model", DeveloperInstructions: "Coordinator role", Sandbox: "read-only", ApprovalPolicy: "never", OnToolCall: func(ctx context.Context, c harness.ToolCall) harness.ToolResult {
			if c.Tool != "ledger_read" {
				t.Errorf("wrong tool: %+v", c)
			}
			called <- struct{}{}
			return harness.ToolResult{Success: true, Text: "scoped result"}
		}}
		var th harness.Thread
		if i == 0 {
			th, err = s.StartThread(ctx, cfg)
		} else {
			th, err = s.ResumeThread(ctx, id, cfg)
		}
		if err != nil {
			t.Fatal(err)
		}
		id = th.ID()
		turn, err := th.StartTurn(ctx, harness.TurnInput{Text: "Review current Issue"})
		if err != nil {
			t.Fatal(err)
		}
		finished := false
		for !finished {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case e := <-th.Events():
				finished = e.Kind == harness.EventTurnCompleted && e.TurnID == turn
			}
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(called) != 2 {
		t.Fatalf("tool callbacks=%d", len(called))
	}
}

func TestManagerDriverRejectsWorkerPermissions(t *testing.T) {
	d := Driver{}
	raw, _ := json.Marshal(Request{Config: harness.ThreadConfig{Sandbox: "danger-full-access", ApprovalPolicy: "never"}})
	if _, err := d.Handle(t.Context(), "start", raw); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("worker permissions were accepted", err)
	}
}

func TestFault_ManagerCloseWaitsForInflightToolEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fake := testkit.NewHarness()
	session, err := fake.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	fake.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		defer close(finished)
		_ = th.CallTool(ctx, turn, "call", "ledger_read", map[string]any{})
		return "interrupted"
	}
	d := Driver{Session: permissionSession{session.(*testkit.FakeSession)}, Profile: protocol.ExecutionProfile{Model: "model"}, Emit: func(_ context.Context, event Event) error {
		if event.Call != nil {
			close(entered)
			<-release
		}
		return nil
	}}
	raw, _ := json.Marshal(Request{Config: harness.ThreadConfig{Sandbox: "read-only", ApprovalPolicy: "never"}})
	info, err := d.Handle(ctx, "start", raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(Request{ThreadID: info.(ThreadInfo).ID, Input: harness.TurnInput{Text: "Read the original ledger"}})
	if _, err := d.Handle(ctx, "turn", raw); err != nil {
		t.Fatal(err)
	}
	<-entered
	closed := make(chan error, 1)
	go func() { _, err := d.Handle(ctx, "close", json.RawMessage(`{}`)); closed <- err }()
	<-session.Done()
	select {
	case err := <-closed:
		close(release)
		t.Fatal("close overtook an inflight event", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("closed process retained a tool callback", ctx.Err())
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result := d.tool(ctx, harness.ToolCall{ThreadID: "old", CallID: "late"}); result.Success {
		t.Fatal("closed driver accepted a late callback")
	}
}

func TestFault_ManagerAttachmentReplaysBacklogBeforeLiveEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := testkit.NewHarness()
	native, _ := fake.Open(ctx)
	transport := &memoryTransport{events: make(chan Event, 300)}
	transport.driver = &Driver{Session: permissionSession{native.(*testkit.FakeSession)}, Profile: protocol.ExecutionProfile{Model: "model"}, Emit: transport.emit}
	for n := int64(1); n <= 257; n++ {
		transport.events <- Event{Sequence: n, ThreadID: "th_1", Native: &harness.Event{Kind: harness.EventAgentMessageDelta, Text: "x"}}
	}
	s := New(ctx, transport)
	deadline := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		count := len(s.orphans["th_1"])
		s.mu.Unlock()
		if count == 257 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("backlog not loaded")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	opened := make(chan error, 1)
	go func() {
		_, err := s.StartThread(ctx, harness.ThreadConfig{Model: "model", Sandbox: "read-only", ApprovalPolicy: "never"})
		opened <- err
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-opened
		t.Fatal("StartThread deadlocked replaying a retained backlog larger than its event buffer")
	}
	cancel()
	<-s.Done()
}

type canonicalSession struct{ permissionSession }
type canonicalThread struct{ permissionThread }

func (t canonicalThread) ModelDefaults() harness.ModelSelection {
	return harness.ModelSelection{Model: "native-model"}
}
func (s canonicalSession) StartThread(ctx context.Context, c harness.ThreadConfig) (harness.Thread, error) {
	t, err := s.permissionSession.StartThread(ctx, c)
	if err != nil {
		return nil, err
	}
	return canonicalThread{t.(permissionThread)}, nil
}
func TestManagerCatalogIDAliasPreservesNativeModelIdentity(t *testing.T) {
	ctx := t.Context()
	fake := testkit.NewHarness()
	fake.Models = []harness.Model{{ID: "catalog-id", Name: "native-model"}}
	s, _ := fake.Open(ctx)
	d := Driver{Session: canonicalSession{permissionSession{s.(*testkit.FakeSession)}}, Profile: protocol.ExecutionProfile{Model: "catalog-id"}, Emit: func(context.Context, Event) error { return nil }}
	raw, _ := json.Marshal(Request{Config: harness.ThreadConfig{Model: "native-model", Sandbox: "read-only", ApprovalPolicy: "never"}})
	_, err := d.Handle(ctx, "start", raw)
	s.Close()
	if err != nil {
		t.Fatalf("valid catalog ID alias rejected: %v", err)
	}
}
