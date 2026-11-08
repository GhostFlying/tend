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
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

const summaryFormatVersion int64 = 3 // Historical fixture format; no producer remains.

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
	if cfg.Purpose == "internal_summary" {
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
	db, err := mainFixtureTemplate.Open(ctx, filepath.Join(t.TempDir(), "tend.db"), clock.Now)
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

func TestHandoffUsesBoundedIndexAndFreshToolset(t *testing.T) {
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
	for _, text := range []string{"index-v1", f.source.ID(), "rollout:" + f.source.ID(), "conversation_context_index"} {
		if !strings.Contains(injected, text) {
			t.Errorf("handoff missing %q", text)
		}
	}
	if len(injected) > protocol.ContextIndexBudget || strings.Contains(injected, "CACHED_SUMMARY_MARKER") || strings.Contains(injected, "ledger_facts") {
		t.Fatal("handoff carried historical prose", injected)
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

func (t *handoffTestThread) ContextUsage(context.Context) (protocol.AgentContextUsage, error) {
	used, window := int64(1000), int64(451250)
	return protocol.AgentContextUsage{Available: true, ActiveTokens: &used, EffectiveWindow: &window}, nil
}
func (t *handoffTestThread) Compact(context.Context) error { return nil }
