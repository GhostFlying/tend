package mainthread

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

type handoffTestSession struct {
	harness.Session
	h                       *testkit.Harness
	mu                      sync.Mutex
	starts, forks, releases int
	startErr, injectErr     error
	resumeErr               map[string]error
	resumeHook              func(context.Context, string, harness.ThreadConfig)
	summaryTurn             func(*testkit.FakeThread, string, harness.TurnInput) string
	summarySessions         []harness.Session
	threads                 map[string]harness.Thread
}

type handoffTestThread struct {
	harness.Thread
	owner    *handoffTestSession
	summary  bool
	nativeID string
}

func (t *handoffTestThread) ID() string {
	if t.nativeID != "" {
		return t.nativeID
	}
	return t.Thread.ID()
}
func (t *handoffTestThread) RolloutPath() string {
	if t.summary {
		return ""
	}
	return "rollout:" + t.ID()
}
func (t *handoffTestThread) ModelDefaults() harness.ModelSelection {
	if v, ok := t.Thread.(harness.ConfiguredThread); ok {
		return v.ModelDefaults()
	}
	return harness.ModelSelection{}
}
func (t *handoffTestThread) InjectDeveloper(ctx context.Context, texts ...string) error {
	t.owner.mu.Lock()
	err := t.owner.injectErr
	t.owner.injectErr = nil
	t.owner.mu.Unlock()
	if err != nil {
		return err
	}
	return t.Thread.InjectDeveloper(ctx, texts...)
}
func (s *handoffTestSession) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	if cfg.Ephemeral {
		return s.ForkThread(ctx, "", cfg)
	}
	s.mu.Lock()
	s.starts++
	err := s.startErr
	s.startErr = nil
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	th, err := s.Session.StartThread(ctx, cfg)
	if err != nil {
		return nil, err
	}
	wrapped := &handoffTestThread{Thread: th, owner: s}
	s.mu.Lock()
	if s.threads == nil {
		s.threads = map[string]harness.Thread{}
	}
	s.threads[th.ID()] = wrapped
	s.mu.Unlock()
	return wrapped, nil
}
func (s *handoffTestSession) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.mu.Lock()
	cached := s.threads[id]
	resumeErr, hook := s.resumeErr[id], s.resumeHook
	s.mu.Unlock()
	if hook != nil {
		hook(ctx, id, cfg)
	}
	if resumeErr != nil {
		return nil, resumeErr
	}
	if cached != nil {
		return cached, nil
	}
	th, err := s.Session.ResumeThread(ctx, id, cfg)
	if err != nil {
		return nil, err
	}
	return &handoffTestThread{Thread: th, owner: s}, nil
}
func (s *handoffTestSession) ForkThread(ctx context.Context, _ string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.mu.Lock()
	s.forks++
	id := fmt.Sprintf("summary-fork-%d", s.forks)
	turn := s.summaryTurn
	s.mu.Unlock()
	handler := cfg.OnToolCall
	if handler != nil {
		cfg.OnToolCall = func(ctx context.Context, c harness.ToolCall) harness.ToolResult {
			c.ThreadID = id
			return handler(ctx, c)
		}
	}
	h := testkit.NewHarness()
	h.Turn = turn
	session, err := h.Open(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.summarySessions = append(s.summarySessions, session)
	s.mu.Unlock()
	th, err := session.StartThread(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &handoffTestThread{Thread: th, owner: s, summary: true, nativeID: id}, nil
}
func (s *handoffTestSession) ReleaseThread(_ context.Context, _ string) error {
	s.mu.Lock()
	s.releases++
	sessions := append([]harness.Session(nil), s.summarySessions...)
	s.summarySessions = nil
	s.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	return nil
}
func (s *handoffTestSession) counts() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts, s.forks, s.releases
}
func (s *handoffTestSession) Close() error {
	_ = s.ReleaseThread(context.Background(), "")
	return s.Session.Close()
}

type handoffFixture struct {
	e       *env
	session *handoffTestSession
	source  harness.Thread
	clock   *testkit.Clock
}

func newHandoffFixture(t *testing.T) *handoffFixture {
	t.Helper()
	ctx := context.Background()
	clock := testkit.NewClock()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	ids := protocol.NewULIDGen(clock.Now, nil)
	a := app.New(db, ids, clock.Now)
	h := testkit.NewHarness()
	base, err := h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session := &handoffTestSession{Session: base, h: h}
	session.summaryTurn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		th.Say(id, "summary-item", "Preserve the user's constraints and continue pending work.")
		return "completed"
	}
	cb := contextbuilder.New(a, db, ids, contextbuilder.Config{})
	m := New(a, cb, tools.New(a, db, cb, quiet()), Config{Instance: "main", Thread: harness.ThreadConfig{Cwd: "/agent"}, Log: quiet(), MaintenanceEnabled: true, SummaryIdleMinutes: 50, Now: clock.Now, SummaryTimeout: time.Second})
	e := &env{t: t, store: db, app: a, ids: ids, now: clock.Now, h: h, m: m}
	source, err := session.StartThread(ctx, m.threadConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindMainThread(ctx, "main", source.ID(), "previous-toolset", rolloutPath(source)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); _ = db.Close() })
	return &handoffFixture{e: e, session: session, source: source, clock: clock}
}
func (f *handoffFixture) saveSummary(t *testing.T) store.HandoffSummary {
	t.Helper()
	snap, err := f.e.app.HandoffSnapshot(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	snap.Summary = &store.SummaryCoverage{Complete: true, Method: "single", Batches: 1}
	summary, err := f.e.app.SaveHandoffSummary(context.Background(), store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "CACHED_SUMMARY_MARKER", FormatVersion: summaryFormatVersion})
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

// logSettlementState preserves the separate durable reasons a handoff can wait.
// It reads only identity and state, not conversation bodies or tool payloads.
func (f *handoffFixture) logSettlementState(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Logf("fixture business time: %s", f.clock.Now().Format(time.RFC3339Nano))
	for _, query := range []struct{ name, sql string }{
		{"outbox", `SELECT COALESCE(json_group_array(json_object('request_id',request_id,'operation',operation,'status',status,'attempts',attempts,'next_attempt_at',next_attempt_at)), '[]') FROM outbox WHERE destination='main_thread' AND status<>'delivered'`},
		{"logical turns", `SELECT COALESCE(json_group_array(json_object('logical_turn_id',logical_turn_id,'status',status,'repair_attempts',repair_attempts,'reason',reason,'revision',revision)), '[]') FROM logical_turns`},
		{"logical executions", `SELECT COALESCE(json_group_array(json_object('request_id',request_id,'logical_turn_id',logical_turn_id,'settled',settled,'repair',repair)), '[]') FROM logical_turn_executions`},
		{"native turns", `SELECT COALESCE(json_group_array(json_object('request_id',t.request_id,'thread_id',t.thread_id,'turn_id',t.turn_id,'status',t.status,'receipt_pending',EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id))), '[]') FROM main_turns t`},
	} {
		var state string
		err := f.e.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, query.sql).Scan(&state)
		})
		t.Logf("%s: %s; read error: %v", query.name, state, err)
	}
}

func TestHandoffUsesCurrentSummaryAndFreshToolset(t *testing.T) {
	f := newHandoffFixture(t)
	f.saveSummary(t)
	hash, err := currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.e.m.openManagedThread(context.Background(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID() == f.source.ID() {
		t.Fatal("reused old native thread")
	}
	created := target.(*handoffTestThread).Thread.(*testkit.FakeThread)
	hasFilesRead := false
	for _, spec := range created.Config.Tools {
		if spec.Name == "files_read" {
			hasFilesRead = true
		}
	}
	if !hasFilesRead || created.Config.OnToolCall == nil {
		t.Fatal("fresh native thread did not receive current managed tools")
	}

	starts, forks, _ := f.session.counts()
	if starts != 2 || forks != 0 {
		t.Fatalf("creates=%d summary forks=%d; cache should avoid model summary", starts, forks)
	}
	injected := strings.Join(f.e.h.Injected(target.ID()), "\n")
	for _, text := range []string{"CACHED_SUMMARY_MARKER", f.source.ID(), "rollout:" + f.source.ID(), "ledger_facts"} {
		if !strings.Contains(injected, text) {
			t.Errorf("handoff missing %q", text)
		}
	}
	binding, ok, err := f.e.app.MainThreadBinding(context.Background(), "main")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if binding.ThreadID != target.ID() || binding.ToolsetHash != hash || len(binding.Ancestors) != 1 || binding.Ancestors[0].ThreadID != f.source.ID() {
		t.Fatalf("binding %+v", binding)
	}
	if len(f.e.h.Injected(f.source.ID())) != 0 || len(businessStarts(f.e.h)) != 0 {
		t.Fatal("summary/handoff polluted source conversation")
	}
}

func TestHandoffRejectedCreateOrInjectPreservesMappingAndRetriesConfirmedStage(t *testing.T) {
	for _, stage := range []string{"create", "inject"} {
		t.Run(stage, func(t *testing.T) {
			f := newHandoffFixture(t)
			f.saveSummary(t)
			f.session.mu.Lock()
			if stage == "create" {
				f.session.startErr = harness.ErrRejected
			} else {
				f.session.injectErr = harness.ErrRejected
			}
			f.session.mu.Unlock()
			if _, err := f.e.m.migrateThread(context.Background(), f.session, f.source, "new-hash", "handoff-retry"); err == nil {
				t.Fatal("missing harness rejection")
			}
			binding, _, _ := f.e.app.MainThreadBinding(context.Background(), "main")
			if binding.ThreadID != f.source.ID() {
				t.Fatal("mapping switched before injection succeeded")
			}
			target, err := f.e.m.migrateThread(context.Background(), f.session, f.source, "new-hash", "handoff-retry")
			if err != nil {
				t.Fatal(err)
			}
			starts, _, _ := f.session.counts()
			want := 2
			if stage == "create" {
				want = 3
			}
			if starts != want {
				t.Fatalf("creates=%d want %d", starts, want)
			}
			if len(f.e.h.Injected(target.ID())) != 1 {
				t.Fatal("handoff package not injected exactly once")
			}
		})
	}
}

func TestHandoffUnknownExternalResultNeverResends(t *testing.T) {
	for _, stage := range []string{"create", "inject"} {
		t.Run(stage, func(t *testing.T) {
			f := newHandoffFixture(t)
			f.saveSummary(t)
			f.session.mu.Lock()
			if stage == "create" {
				f.session.startErr = errors.New("lost create response")
			} else {
				f.session.injectErr = errors.New("lost inject response")
			}
			f.session.mu.Unlock()
			if _, err := f.e.m.migrateThread(context.Background(), f.session, f.source, "new-hash", "handoff-unknown"); err == nil {
				t.Fatal("expected unknown failure")
			}
			before, _, _ := f.session.counts()
			if _, err := f.e.m.migrateThread(context.Background(), f.session, f.source, "new-hash", "handoff-unknown"); err == nil {
				t.Fatal("unknown request unexpectedly retried")
			}
			after, _, _ := f.session.counts()
			if before != after {
				t.Fatal("unconfirmed create resent")
			}
			binding, _, _ := f.e.app.MainThreadBinding(context.Background(), "main")
			if binding.ThreadID != f.source.ID() {
				t.Fatal("unknown result changed source authority")
			}
			pending, ok, err := f.e.app.PendingHandoff(context.Background(), "main")
			if err != nil || !ok || pending.Phase != "unknown" {
				t.Fatalf("pending %+v %v", pending, err)
			}
		})
	}
}

func TestSummaryRejectsToolsAndNeverAppendsSourceOrWebMessages(t *testing.T) {
	f := newHandoffFixture(t)
	attempt := make(chan harness.ToolResult, 1)
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		if th.Config.Sandbox != "read-only" || th.Config.ApprovalPolicy != "never" || len(th.Config.Tools) != 0 {
			t.Error("summary branch not isolated")
		}
		attempt <- th.CallTool(context.Background(), id, "mutate-call", "decisions_open", map[string]any{"malformed": true})
		th.Say(id, "summary", "Private summary")
		return "completed"
	}
	summary, ok, err := f.e.m.prepareSummary(context.Background(), f.session, f.source)
	if err != nil || !ok {
		t.Fatalf("summary %+v %v", summary, err)
	}
	if res := <-attempt; res.Success || !strings.Contains(res.Text, "disabled") {
		t.Fatalf("tool result %+v", res)
	}
	if f.e.count(`SELECT COUNT(*) FROM tasks`) != 0 || f.e.count(`SELECT COUNT(*) FROM conversation_messages`) != 0 {
		t.Fatal("private summary caused public side effects")
	}
	if len(f.e.h.Injected(f.source.ID())) != 0 || len(businessStarts(f.e.h)) != 0 {
		t.Fatal("maintenance prompt or answer entered source history")
	}
	_, forks, releases := f.session.counts()
	if forks != 1 || releases != 1 {
		t.Fatalf("forks=%d releases=%d", forks, releases)
	}
}

func TestSummaryInPlaceMessageUpdateInvalidatesCoverage(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "assistant-item", Author: "agent", Body: "initial", CreatedAt: f.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	original := f.saveSummary(t)
	before := original.Snapshot
	f.clock.Advance(time.Second)
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conversation_messages SET body='updated cumulative response' WHERE message_id='assistant-item'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if before.MaxSeq != after.MaxSeq || before.Version == after.Version || before.Covers(after) {
		t.Fatalf("coverage not invalidated: before=%+v after=%+v", before, after)
	}
	_, ok, err := f.e.m.prepareSummary(ctx, f.session, f.source)
	if err != nil || !ok {
		t.Fatal(err)
	}
	_, forks, _ := f.session.counts()
	if forks != 1 {
		t.Fatal("stale cached summary was reused")
	}
}

// A completion notice is a ledger observation, not a native user turn. The
// private summary must receive its body before claiming the ledger watermark.
func TestSummaryReceivesSystemNoticeAndSubsequentInPlaceUpdate(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "completion-notice", Author: "system", Body: "TASK_COMPLETION_NOTICE_MARKER: independent acceptance passed", CreatedAt: f.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inputs := make(chan string, 2)
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		inputs <- in.Text
		th.Say(id, "summary", "Summary including server observations")
		return "completed"
	}
	first, ok, err := f.e.m.prepareSummary(ctx, f.session, f.source)
	if err != nil || !ok {
		t.Fatalf("first summary: ok=%v err=%v", ok, err)
	}
	if text := <-inputs; !strings.Contains(text, "TASK_COMPLETION_NOTICE_MARKER: independent acceptance passed") {
		t.Fatal("summary claimed coverage without receiving server-only completion notice body")
	}
	if len(businessStarts(f.e.h)) != 0 || len(f.e.h.Injected(f.source.ID())) != 0 {
		t.Fatal("server notice was unexpectedly delivered to source native history")
	}
	f.clock.Advance(time.Second)
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conversation_messages SET body=? WHERE message_id='completion-notice'`, "UPDATED_COMPLETION_NOTICE_MARKER: owner follow-up remains required")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	second, ok, err := f.e.m.prepareSummary(ctx, f.session, f.source)
	if err != nil || !ok {
		t.Fatalf("updated summary: ok=%v err=%v", ok, err)
	}
	if text := <-inputs; !strings.Contains(text, "UPDATED_COMPLETION_NOTICE_MARKER: owner follow-up remains required") {
		t.Fatal("summary claimed new version without receiving updated observation body")
	}
	if first.Snapshot.MaxSeq != second.Snapshot.MaxSeq || first.Snapshot.Version == second.Snapshot.Version {
		t.Fatal("fixture did not exercise an in-place update at the same sequence")
	}
	current, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Snapshot.Covers(current) {
		t.Fatal("fully supplied observations did not produce current coverage")
	}
}

func TestSummaryLargeCorrectionUsesCleanBatches(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := t.Context()
	f.saveSummary(t)
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "large-server-observation", Author: "system", Body: strings.Repeat("B", (256<<10)+1)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		calls++
		th.Say(id, "summary", "Known obligations retained; batch merged")
		return "completed"
	}
	result, ok, err := f.e.m.prepareSummary(ctx, f.session, f.source)
	if err != nil || !ok {
		t.Fatal(result, ok, err)
	}
	if calls < 3 || result.Snapshot.Summary == nil || !result.Snapshot.Summary.Complete || result.Snapshot.Summary.Method != "two_stage" || result.Snapshot.Summary.Batches < 2 {
		t.Fatal("large input lost batch coverage", calls, result.Snapshot)
	}
	current, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil || !result.Snapshot.Covers(current) {
		t.Fatal("complete batches did not cover frozen source", err)
	}
}

func TestLegacySummaryDoesNotSuppressCompleteObservationInput(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "older-server-notice", Author: "system", Body: "LEGACY_MISSING_OBSERVATION_MARKER", CreatedAt: f.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "Legacy prose omitted server-only observations", FormatVersion: 1}); err != nil {
		t.Fatal(err)
	}
	input := make(chan string, 1)
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		input <- in.Text
		th.Say(id, "summary", "Rebuilt complete summary")
		return "completed"
	}
	rebuilt, ok, err := f.e.m.prepareSummary(ctx, f.session, f.source)
	if err != nil || !ok {
		t.Fatalf("rebuild legacy summary: ok=%v err=%v", ok, err)
	}
	if rebuilt.FormatVersion < 2 {
		t.Fatal("legacy format was reused as complete summary")
	}
	select {
	case text := <-input:
		if !strings.Contains(text, "LEGACY_MISSING_OBSERVATION_MARKER") {
			t.Fatal("legacy baseline skipped full observation replay")
		}
	default:
		t.Fatal("legacy cached summary prevented regeneration")
	}
}

func TestHandoffDeliveryClassifiesRejectedAndUnknownHarnessResults(t *testing.T) {
	for _, stage := range []string{"create", "inject"} {
		for _, uncertain := range []bool{false, true} {
			name := stage + "-rejected"
			if uncertain {
				name = stage + "-unknown"
			}
			t.Run(name, func(t *testing.T) {
				f := newHandoffFixture(t)
				f.saveSummary(t)
				f.e.m.mu.Lock()
				f.e.m.session = f.session
				f.e.m.thread = f.source
				f.e.m.mu.Unlock()
				failure := harness.ErrRejected
				if uncertain {
					failure = errors.New("external outcome unconfirmed")
				}
				f.session.mu.Lock()
				if stage == "create" {
					f.session.startErr = failure
				} else {
					f.session.injectErr = failure
				}
				f.session.mu.Unlock()
				delivery := app.Delivery{RequestID: "handoff-delivery", Operation: "conversation.handoff", Payload: []byte(`{}`)}
				result := f.e.m.Deliver(context.Background(), delivery)
				want := app.Retry
				if uncertain {
					want = app.Unknown
				}
				if result.Outcome != want {
					t.Fatalf("delivery outcome=%v want=%v reason=%s", result.Outcome, want, result.Reason)
				}
				binding, _, err := f.e.app.MainThreadBinding(context.Background(), "main")
				if err != nil || binding.ThreadID != f.source.ID() {
					t.Fatal("failed delivery changed source mapping")
				}
				if uncertain {
					before, _, _ := f.session.counts()
					again := f.e.m.Deliver(context.Background(), delivery)
					after, _, _ := f.session.counts()
					if again.Outcome != app.Unknown || before != after {
						t.Fatal("uncertain delivery resent external create/injection")
					}
				} else if retried := f.e.m.Deliver(context.Background(), delivery); retried.Outcome != app.Delivered {
					t.Fatalf("confirmed rejected stage did not recover: %+v", retried)
				}
			})
		}
	}
}

func TestHandoffRetryRefreshesSourceBeyondRecentMessageFocus(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	hash, err := currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE main_thread_bindings SET toolset_hash=? WHERE instance='main' AND thread_id=?`, hash, f.source.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.saveSummary(t)
	f.e.h.Turn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		if in.Repair {
			return completeHistoricalFixtureReview(t, f.e, th, id)
		}
		th.Say(id, "owner-reply", "Acknowledged the owner's instruction.")
		return "completed"
	}
	f.e.m.cfg.Open = func(context.Context) (harness.Session, error) { return f.session, nil }
	f.e.m.cfg.MaintenanceInterval = time.Hour
	f.e.signal = dispatch.NewSignal()
	f.e.start()
	defer f.e.stop()
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := f.e.m.waitThread(ready); err != nil {
		t.Fatal(err)
	}
	f.session.mu.Lock()
	f.session.injectErr = harness.ErrRejected
	f.session.mu.Unlock()
	const jobID = "handoff-refresh-job"
	if _, err := f.e.m.migrateThread(ctx, f.session, f.source, hash, jobID); !errors.Is(err, harness.ErrRejected) {
		t.Fatalf("initial injection=%v", err)
	}
	pending, exists, err := f.e.app.GetHandoff(ctx, jobID)
	if err != nil || !exists || pending.Phase != "created" {
		t.Fatalf("pending %+v %v", pending, err)
	}
	const constraint = "PERSISTENT_NEW_CONSTRAINT_MARKER: never publish without owner review"
	// Eleven further exchanges push this constraint beyond the default eight
	// focused messages. A refreshed full summary must still preserve it.
	for i := 0; i < 12; i++ {
		body := fmt.Sprintf("Later source exchange %d", i)
		if i == 0 {
			body = constraint
		}
		message := f.e.say(body)
		want := i + 1
		eventually(t, "source completes owner exchange", func() bool {
			return f.e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND status='completed'`) == want
		})
		// Native completion leaves classification and fact review outstanding.
		// Finish this exchange's real repair before queuing the next owner input,
		// so the final handoff check does not drain twelve repairs in one budget.
		eventually(t, "source completes logical exchange", func() bool {
			var status string
			if err := f.e.store.Read(ctx, func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(ctx, `SELECT status FROM logical_turns WHERE message_id=?`, message).Scan(&status)
			}); err != nil {
				t.Fatal(err)
			}
			return status == "completed"
		})
	}
	var lastSnapshot store.HandoffSnapshot
	var snapshotErr, logicalErr error
	var unfinished int
	settled := false
	defer func() {
		if !settled {
			t.Logf("last handoff snapshot: %+v; snapshot error: %v; unfinished logical turns: %d; logical read error: %v", lastSnapshot, snapshotErr, unfinished, logicalErr)
			f.logSettlementState(t)
		}
	}()
	eventually(t, "owner delivery outbox settles", func() bool {
		// A delivery can be retried while the preceding native turn settles.
		// Drive the frozen fixture clock to its durable retry deadline.
		var next int64
		if err := f.e.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MIN(next_attempt_at),0) FROM outbox WHERE destination='main_thread' AND status='pending' AND next_attempt_at>?`, f.clock.Now().UnixMilli()).Scan(&next)
		}); err != nil {
			t.Fatal(err)
		}
		if delay := time.UnixMilli(next).Sub(f.clock.Now()); delay > 0 {
			f.clock.Advance(delay)
		}
		lastSnapshot, snapshotErr = f.e.app.HandoffSnapshot(ctx, "main")
		logicalErr = f.e.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM logical_turns WHERE status<>'completed'`).Scan(&unfinished)
		})
		return snapshotErr == nil && !lastSnapshot.Pending && !lastSnapshot.Active && logicalErr == nil && unfinished == 0
	})
	settled = true
	if len(businessStarts(f.e.h)) != 12 {
		t.Fatal("fixture did not process all source native turns")
	}
	summaryInput := make(chan string, 1)
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		summaryInput <- in.Text
		th.Say(id, "fresh-summary", "UPDATED_SUMMARY_CONSTRAINT_MARKER: never publish without owner review")
		return "completed"
	}
	target, err := f.e.m.migrateThread(ctx, f.session, f.source, hash, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID() != pending.TargetThreadID {
		t.Fatal("retry created another target instead of reusing confirmed thread")
	}
	starts, forks, _ := f.session.counts()
	if starts != 2 || forks != 1 {
		t.Fatalf("native creates=%d summary forks=%d", starts, forks)
	}
	if input := <-summaryInput; strings.Contains(input, constraint) {
		t.Fatal("confirmed native history was replayed in the correction supplement")
	}
	injected := strings.Join(f.e.h.Injected(target.ID()), "\n")
	if !strings.Contains(injected, "UPDATED_SUMMARY_CONSTRAINT_MARKER") || !strings.Contains(injected, `"summary_covers_snapshot":true`) {
		t.Fatal("retry injected stale prepared prompt instead of fresh summary package")
	}
	committed, exists, err := f.e.app.GetHandoff(ctx, jobID)
	if err != nil || !exists || committed.Phase != "committed" || committed.SourceVersion <= pending.SourceVersion {
		t.Fatalf("refreshed job %+v %v", committed, err)
	}
}

func (t *handoffTestThread) ContextUsage(context.Context) (protocol.AgentContextUsage, error) {
	used, window := int64(1000), int64(451250)
	return protocol.AgentContextUsage{Available: true, ActiveTokens: &used, EffectiveWindow: &window}, nil
}
func (t *handoffTestThread) Compact(context.Context) error { return nil }
