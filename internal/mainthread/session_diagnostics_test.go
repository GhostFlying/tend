package mainthread

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type lifecycleLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lifecycleLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lifecycleLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type lifecycleSession struct {
	harness.Session
	entered chan struct{}
	release chan struct{}
	err     error
	closes  int
}

func (s *lifecycleSession) Close() error {
	s.closes++
	close(s.entered)
	<-s.release
	_ = s.Session.Close()
	return s.err
}

func TestManagerSessionLogsExitBeforeBlockedCleanup(t *testing.T) {
	for _, cause := range []string{"event_stream_ended", "context_cancelled"} {
		t.Run(cause, func(t *testing.T) {
			h := testkit.NewHarness()
			e := newEnv(t, h)
			e.stop()
			e.m.startupExited = make(chan error, 1)
			logs := &lifecycleLog{}
			e.m.cfg.Log = slog.New(slog.NewJSONHandler(logs, nil))
			s := &lifecycleSession{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("cleanup close failure")}
			e.m.cfg.Open = func(ctx context.Context) (harness.Session, error) {
				var err error
				s.Session, err = h.Open(ctx)
				return s, err
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			done := make(chan error, 1)
			go func() { done <- e.m.serve(ctx) }()
			wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			th, err := e.m.waitStartup(wait)
			if err != nil || th == nil {
				t.Fatal("startup", th, err)
			}
			e.m.mu.Lock()
			e.m.active = &turn{requestID: "original-request", sourceRequestID: "original-source", messageID: "original-message", turnID: "original-turn", inputText: "private prompt never logged", done: make(chan string, 1)}
			e.m.mu.Unlock()
			if cause == "context_cancelled" {
				cancel(errors.New("original cancellation cause"))
			} else {
				h.Crash()
			}
			select {
			case <-s.entered:
			case <-wait.Done():
				t.Fatal("cleanup did not start")
			}
			// The original cause must survive even when Close cannot finish.
			before := logs.text()
			close(s.release)
			select {
			case err = <-done:
			case <-wait.Done():
				t.Fatal("cleanup did not finish")
			}
			if !strings.Contains(before, `"msg":"manager session exiting"`) {
				t.Fatalf("exit missing before Close returns: %s", before)
			}
			if cause == "context_cancelled" {
				if !errors.Is(err, context.Canceled) || !strings.Contains(before, "original cancellation cause") {
					t.Fatal("original context cause lost", err, before)
				}
			} else if err == nil || !strings.Contains(err.Error(), "harness ended the thread") {
				t.Fatal("original serve error changed", err)
			}
			if s.closes != 1 || !strings.Contains(logs.text(), `"close_error":"cleanup close failure"`) || strings.Contains(logs.text(), "private prompt never logged") {
				t.Fatal("close behavior/error separation/privacy", s.closes, logs.text())
			}
		})
	}
}

func TestManagerSessionDiagnosticsFreezeAttachmentIdentity(t *testing.T) {
	logs := &lifecycleLog{}
	m := &Manager{cfg: Config{Instance: "original-instance", ConversationID: "original-conversation", Log: slog.New(slog.NewJSONHandler(logs, nil))}}
	m.active = &turn{requestID: "original-request", turnID: "original-turn"}
	d := m.sessionDiagnostics(&hostedconfig.Selection{IssueID: "original-issue", RunnerRunID: "original-run", AssignmentEpoch: 2})
	d.exit(t.Context(), errors.New("original serve failure"))
	m.active = &turn{requestID: "replacement-request", turnID: "replacement-turn"}
	m.named = &hostedconfig.Selection{IssueID: "replacement-issue", RunnerRunID: "replacement-run"}
	d.cleanup(t.Context(), slog.LevelWarn, "final_event", context.DeadlineExceeded)
	for _, line := range strings.Split(strings.TrimSpace(logs.text()), "\n") {
		for _, identity := range []string{"original-issue", "original-run"} {
			if !strings.Contains(line, identity) {
				t.Fatal("attachment identity lost", identity, line)
			}
		}
		if strings.Contains(line, "replacement-") {
			t.Fatal("replacement attributed to original attachment", line)
		}
	}
}

type lifecycleTransport struct {
	closes int
	err    error
}

func (t *lifecycleTransport) Call(_ context.Context, method string, _, _ any) error {
	if method != "close" {
		return errors.New("unexpected native work")
	}
	t.closes++
	return t.err
}

func (*lifecycleTransport) Events(ctx context.Context, _ int64) ([]coordination.Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestManagerSessionDiagnosticsPreserveCoordinatorCloseOnce(t *testing.T) {
	logs := &lifecycleLog{}
	m := &Manager{cfg: Config{Log: slog.New(slog.NewJSONHandler(logs, nil))}}
	tr := &lifecycleTransport{err: errors.New("original close outcome unknown")}
	s := coordination.New(t.Context(), tr)
	d := m.sessionDiagnostics(nil)
	for _, trigger := range []string{"serve_final_drain", "serve_cleanup"} {
		if err := d.close(t.Context(), s, trigger); err != tr.err {
			t.Fatal("native close outcome changed", err)
		}
	}
	if tr.closes != 1 {
		t.Fatal("logging repeated the physical request", tr.closes)
	}
}

func TestManagerSessionLogsExplicitCloseInitiatingRequest(t *testing.T) {
	logs := &lifecycleLog{}
	m := &Manager{cfg: Config{Log: slog.New(slog.NewJSONHandler(logs, nil))}}
	tr := &lifecycleTransport{}
	s := coordination.New(t.Context(), tr)
	if err := m.closeSession(t.Context(), s, "handoff-request", "handoff_completed"); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.text()), "\n") {
		if !strings.Contains(line, `"initiating_request_id":"handoff-request"`) {
			t.Fatal("known close request was omitted without an active turn", line)
		}
	}
}

func TestManagerSessionLogsPoolExitBeforeCancellation(t *testing.T) {
	for _, startupFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_delivery", true: "startup_failed"}[startupFailure], func(t *testing.T) {
			h := testkit.NewHarness()
			e := newEnv(t, h)
			e.stop()
			e.m.startupExited = make(chan error, 1)
			logs := &lifecycleLog{}
			e.m.cfg.Log = slog.New(slog.NewJSONHandler(logs, nil))
			if startupFailure {
				e.m.cfg.Open = func(context.Context) (harness.Session, error) {
					return nil, errors.New("original startup failure")
				}
			}
			pool := &BackgroundPool{App: e.app}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			pool.drain(ctx, "main", e.m)
			output := logs.text()
			phase := "delivery_unavailable"
			if startupFailure {
				phase = "startup_failed"
				if !strings.Contains(output, `"pool_error":"original startup failure"`) {
					t.Fatal("original pool error was lost", output)
				}
			}
			if !strings.Contains(output, `"pool_exit_phase":"`+phase+`"`) || !strings.Contains(output, `"msg":"manager pool attachment ending"`) {
				t.Fatal("pool cancellation cause was lost", output)
			}
			if !startupFailure && strings.Index(output, "manager pool attachment ending") >= strings.Index(output, "manager session exiting") {
				t.Fatal("pool release was logged only after cancelling its session", output)
			}
		})
	}
}

func TestManagerEventDiagnosticsPreserveOriginalSequence(t *testing.T) {
	logs := &lifecycleLog{}
	m := &Manager{cfg: Config{Log: slog.New(slog.NewJSONHandler(logs, nil))}}
	d := m.sessionDiagnostics(&hostedconfig.Selection{IssueID: "issue", RunnerRunID: "reader-run"})
	s, err := mainFixtureTemplate.Open(t.Context(), filepath.Join(t.TempDir(), "events.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var observed store.WriteObservation
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observed = o })(s)
	ev := harness.Event{CoordinatorSequence: 2, OriginRunID: "original-run", OriginSequence: 14, Kind: harness.EventItemCompleted, Text: "private event body"}
	ctx := store.WithWriteSource(t.Context(), store.WriteSource{RequestID: "original-request", TraceID: "original-trace"})
	ctx = d.eventContext(ctx, ev)
	// A nested command may replace request metadata; its original event scope
	// must survive independently of that command's operation name.
	ctx = store.WithWriteSource(ctx, store.WriteSource{Operation: "nested.command", RequestID: "nested-request", TraceID: "original-trace"})
	if err := s.Write(ctx, func(*store.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if observed.Source.RunID != "original-run" || observed.Source.EventSequence != 14 || observed.Source.AttachmentRunID != "reader-run" || observed.Source.CoordinatorSequence != 2 || observed.Source.Operation != "nested.command" || observed.Source.RequestID != "nested-request" || observed.Source.TraceID != "original-trace" {
		t.Fatalf("event attribution = %+v", observed)
	}
	d.eventError(ctx, ev, context.DeadlineExceeded)
	text := logs.text()
	if !strings.Contains(text, `"event_sequence":14`) || !strings.Contains(text, `"source_run_id":"original-run"`) || strings.Contains(text, ev.Text) {
		t.Fatalf("unsafe or incomplete event failure: %s", text)
	}
}
