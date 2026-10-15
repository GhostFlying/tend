package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

const dest = "test_dest"

// queueCommand queues one outbox message per entry of its payload.
type queueCommand struct{}

func (queueCommand) Operation() string { return "test.queue" }
func (queueCommand) Authorize(context.Context, *app.Exec, store.Querier) error {
	return nil
}
func (queueCommand) Apply(_ context.Context, x *app.Exec, _ *store.Tx) (app.Outcome, error) {
	var names []string
	if err := json.Unmarshal(x.Env.Payload, &names); err != nil {
		return app.Outcome{}, err
	}
	out := app.Outcome{Result: map[string]any{"queued": len(names)}}
	for _, n := range names {
		out.Outbox = append(out.Outbox, app.OutboxMessage{Destination: dest, Operation: "test.deliver", Payload: map[string]string{"name": n}})
	}
	return out, nil
}

// fakeHandler records deliveries and answers with scripted outcomes.
type fakeHandler struct {
	mu        sync.Mutex
	delivered []app.Delivery
	reconcile []app.Delivery
	// deliver decides the outcome of a delivery; nil means Delivered.
	deliver func(app.Delivery) app.DeliveryResult
	// reconciled is the outcome of every reconciliation.
	reconciled app.DeliveryResult
	got        chan app.Delivery
}

func (h *fakeHandler) Deliver(_ context.Context, d app.Delivery) app.DeliveryResult {
	h.mu.Lock()
	h.delivered = append(h.delivered, d)
	h.mu.Unlock()
	if h.got != nil {
		h.got <- d
	}
	if h.deliver != nil {
		return h.deliver(d)
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}

func (h *fakeHandler) Reconcile(_ context.Context, d app.Delivery) app.DeliveryResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reconcile = append(h.reconcile, d)
	return h.reconciled
}

func (h *fakeHandler) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, d := range h.delivered {
		var p struct{ Name string }
		if err := json.Unmarshal(d.Payload, &p); err != nil {
			panic(err)
		}
		out = append(out, p.Name)
	}
	return out
}

type env struct {
	t      *testing.T
	store  *store.Store
	app    *app.App
	clock  *testkit.Clock
	ids    protocol.IDGen
	signal *Signal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := testkit.NewClock()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ids := testkit.NewIDGen(clock, 7)
	sig := NewSignal()
	return &env{t: t, store: s, app: app.New(s, ids, clock.Now, app.WithNotifier(sig)), clock: clock, ids: ids, signal: sig}
}

// dispatcher returns a fresh dispatcher, as a restarted process would build.
func (e *env) dispatcher(h Handler) *Dispatcher {
	return New(e.app, map[string]Handler{dest: h}, e.signal, e.clock.Now, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (e *env) queue(names ...string) {
	e.t.Helper()
	payload, err := json.Marshal(names)
	if err != nil {
		e.t.Fatal(err)
	}
	envl := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "test.queue", Target: protocol.Target{Type: "test"}, Payload: payload}
	if _, err := e.app.Execute(context.Background(), protocol.Principal{Kind: protocol.PrincipalSystem}, envl, queueCommand{}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) outbox() []store.OutboxMessage {
	e.t.Helper()
	var out []store.OutboxMessage
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		for _, st := range []string{store.OutboxPending, store.OutboxSending, store.OutboxDelivered, store.OutboxFailed, store.OutboxUnknown} {
			msgs, err := store.ListOutboxByStatus(context.Background(), tx.Q(), st)
			if err != nil {
				return err
			}
			out = append(out, msgs...)
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) events(kind string) int {
	e.t.Helper()
	var n int
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n)
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) drain(d *Dispatcher) time.Time {
	e.t.Helper()
	wake, err := d.Drain(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return wake
}

func TestFault_CrashAfterCommitBeforeDispatch(t *testing.T) {
	e := newEnv(t)
	e.queue("a")
	// The process that committed the message exits before delivering it.
	queued := e.outbox()
	if len(queued) != 1 || queued[0].Status != store.OutboxPending {
		t.Fatalf("outbox = %+v", queued)
	}

	h := &fakeHandler{}
	d := e.dispatcher(h)
	if err := d.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.drain(d)
	e.drain(d)
	if len(h.delivered) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(h.delivered))
	}
	if got := h.delivered[0]; got.RequestID != queued[0].RequestID || got.ParentRequestID != queued[0].ParentRequestID {
		t.Errorf("delivered under %s (parent %s), want the queued identity %s (parent %s)",
			got.RequestID, got.ParentRequestID, queued[0].RequestID, queued[0].ParentRequestID)
	}
	if st := e.outbox()[0].Status; st != store.OutboxDelivered {
		t.Errorf("status = %s, want delivered", st)
	}
}

func TestFault_CrashDuringDispatch(t *testing.T) {
	cases := []struct {
		name       string
		reconciled app.DeliveryOutcome
		wantStatus string
		wantSends  int
		wantEvent  string
	}{
		{"unknown is not resent", app.Unknown, store.OutboxUnknown, 0, "outbox.unknown"},
		{"confirmed delivery is not resent", app.Delivered, store.OutboxDelivered, 0, ""},
		{"confirmed non-delivery is resent", app.Retry, store.OutboxDelivered, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.queue("a")
			// The previous process claimed the message and died mid-send.
			claimed, _, ok, err := e.app.NextDelivery(context.Background(), dest)
			if err != nil || !ok {
				t.Fatalf("claim: ok=%v err=%v", ok, err)
			}

			h := &fakeHandler{reconciled: app.DeliveryResult{Outcome: tc.reconciled, Reason: "no record of the message"}}
			d := e.dispatcher(h)
			if err := d.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			e.drain(d)
			if len(h.reconcile) != 1 || h.reconcile[0].RequestID != claimed.RequestID {
				t.Fatalf("reconciled %+v, want the claimed message", h.reconcile)
			}
			if len(h.delivered) != tc.wantSends {
				t.Fatalf("sends after recovery = %d, want %d", len(h.delivered), tc.wantSends)
			}
			if tc.wantSends > 0 {
				if got := h.delivered[0]; got.RequestID != claimed.RequestID || got.Attempt != 2 {
					t.Errorf("resent as %s attempt %d, want %s attempt 2", got.RequestID, got.Attempt, claimed.RequestID)
				}
			}
			if st := e.outbox()[0].Status; st != tc.wantStatus {
				t.Errorf("status = %s, want %s", st, tc.wantStatus)
			}
			if tc.wantEvent != "" && e.events(tc.wantEvent) != 1 {
				t.Errorf("%s events = %d, want 1", tc.wantEvent, e.events(tc.wantEvent))
			}
		})
	}
}

func TestRetryKeepsOrderAndBacksOff(t *testing.T) {
	e := newEnv(t)
	e.queue("a", "b")
	fails := 2
	h := &fakeHandler{deliver: func(d app.Delivery) app.DeliveryResult {
		var p struct{ Name string }
		_ = json.Unmarshal(d.Payload, &p)
		if p.Name == "a" && fails > 0 {
			fails--
			return app.DeliveryResult{Outcome: app.Retry}
		}
		return app.DeliveryResult{Outcome: app.Delivered}
	}}
	d := e.dispatcher(h)

	start := e.clock.Now()
	if wake := e.drain(d); !wake.Equal(start.Add(time.Second)) {
		t.Errorf("wake after first failure = %v, want +1s", wake.Sub(start))
	}
	if got := h.names(); len(got) != 1 {
		t.Fatalf("delivered %v: b must wait behind a", got)
	}
	e.clock.Advance(time.Second)
	if wake := e.drain(d); !wake.Equal(start.Add(3 * time.Second)) {
		t.Errorf("wake after second failure = %v, want +3s (backoff doubled)", wake.Sub(start))
	}
	e.clock.Advance(2 * time.Second)
	if wake := e.drain(d); !wake.IsZero() {
		t.Errorf("wake with nothing queued = %v, want zero", wake)
	}
	if got, want := h.names(), []string{"a", "a", "a", "b"}; !slices.Equal(got, want) {
		t.Errorf("delivery order = %v, want %v", got, want)
	}
}

func TestFailedMessageDoesNotBlockQueue(t *testing.T) {
	e := newEnv(t)
	e.queue("a", "b")
	h := &fakeHandler{deliver: func(d app.Delivery) app.DeliveryResult {
		if string(d.Payload) == `{"name":"a"}` {
			return app.DeliveryResult{Outcome: app.Failed, Reason: "rejected"}
		}
		return app.DeliveryResult{Outcome: app.Delivered}
	}}
	e.drain(e.dispatcher(h))
	if got := h.names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("delivered %v, want [a b]", got)
	}
	if n := e.events("outbox.failed"); n != 1 {
		t.Errorf("outbox.failed events = %d, want 1", n)
	}
}

func TestSendingHeadBlocksUntilReconciled(t *testing.T) {
	e := newEnv(t)
	e.queue("a", "b")
	if _, _, ok, err := e.app.NextDelivery(context.Background(), dest); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	h := &fakeHandler{reconciled: app.DeliveryResult{Outcome: app.Retry}}
	d := e.dispatcher(h)
	e.drain(d)
	if got := h.names(); len(got) != 0 {
		t.Fatalf("delivered %v while a was in flight, want nothing", got)
	}
	if err := d.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.drain(d)
	if got := h.names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("delivered %v after reconciliation, want [a b]", got)
	}
}

func TestReconcileRetryKeepsDelay(t *testing.T) {
	e := newEnv(t)
	e.queue("a")
	if _, _, ok, err := e.app.NextDelivery(context.Background(), dest); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	h := &fakeHandler{reconciled: app.DeliveryResult{Outcome: app.Retry, RetryAfter: time.Minute}}
	d := e.dispatcher(h)
	if err := d.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if wake := e.drain(d); !wake.Equal(e.clock.Now().Add(time.Minute)) || len(h.delivered) != 0 {
		t.Fatalf("wake = %v, deliveries = %d; want the requested minute and none yet", wake, len(h.delivered))
	}
}

// blockingHandler holds every delivery until released.
type blockingHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h *blockingHandler) Deliver(ctx context.Context, _ app.Delivery) app.DeliveryResult {
	h.started <- struct{}{}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}

func (h *blockingHandler) Reconcile(context.Context, app.Delivery) app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Unknown}
}

// slowQueueCommand queues one message for the slow destination.
type slowQueueCommand struct{}

func (slowQueueCommand) Operation() string { return "test.queue_slow" }
func (slowQueueCommand) Authorize(context.Context, *app.Exec, store.Querier) error {
	return nil
}
func (slowQueueCommand) Apply(context.Context, *app.Exec, *store.Tx) (app.Outcome, error) {
	return app.Outcome{Result: map[string]any{}, Outbox: []app.OutboxMessage{{Destination: "slow", Operation: "test.deliver", Payload: map[string]string{}}}}, nil
}

func TestSlowDestinationDoesNotBlockOthers(t *testing.T) {
	e := newEnv(t)
	slow := &blockingHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	fast := &fakeHandler{got: make(chan app.Delivery, 1)}
	d := New(e.app, map[string]Handler{dest: fast, "slow": slow}, e.signal, e.clock.Now, Config{PollInterval: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		close(slow.release)
		cancel()
		<-done
	}()

	envl := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "test.queue_slow", Target: protocol.Target{Type: "test"}}
	if _, err := e.app.Execute(context.Background(), protocol.Principal{Kind: protocol.PrincipalSystem}, envl, slowQueueCommand{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slow.started:
	case <-time.After(5 * time.Second):
		t.Fatal("slow destination never started")
	}
	e.queue("a")
	select {
	case <-fast.got:
	case <-time.After(5 * time.Second):
		t.Fatal("fast destination was held back by the slow one")
	}
}

func TestOrderFollowsCommitNotClock(t *testing.T) {
	e := newEnv(t)
	e.queue("a")
	// The next request read an earlier clock value but commits later.
	e.clock.Advance(-time.Second)
	e.queue("b")
	e.clock.Advance(time.Second)
	h := &fakeHandler{}
	e.drain(e.dispatcher(h))
	if got := h.names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("delivery order = %v, want commit order [a b]", got)
	}
}

func TestBackoff(t *testing.T) {
	c := Config{BaseBackoff: time.Second, MaxBackoff: 10 * time.Second}
	for attempt, want := range map[int64]time.Duration{1: time.Second, 2: 2 * time.Second, 4: 8 * time.Second, 5: 10 * time.Second, 60: 10 * time.Second} {
		if got := c.Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestRunDeliversOnNotifyAndStops(t *testing.T) {
	e := newEnv(t)
	h := &fakeHandler{got: make(chan app.Delivery, 1)}
	d := New(e.app, map[string]Handler{dest: h}, e.signal, e.clock.Now, Config{PollInterval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	e.queue("a")
	select {
	case <-h.got:
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered after the commit notification")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
