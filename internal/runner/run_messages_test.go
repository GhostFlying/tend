package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type messageTransport struct {
	mu    sync.Mutex
	m     protocol.RunMessage
	calls map[string][]protocol.Envelope
	lose  map[string]int
	order []string
	after func(string)
}

func (s *messageTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get(headerRun) != s.m.RunID || r.Header.Get("Authorization") != "Bearer run-token" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		page := protocol.RunMessagePage{Messages: []protocol.RunMessage{}}
		if s.m.Status == protocol.RunMessageQueued || s.m.Status == protocol.RunMessageDispatching || s.m.Status == protocol.RunMessageUnknown {
			page.Messages = append(page.Messages, s.m)
		}
		_ = json.NewEncoder(w).Encode(page)
		return
	}
	var env protocol.Envelope
	_ = json.NewDecoder(r.Body).Decode(&env)
	s.calls[env.Operation] = append(s.calls[env.Operation], env)
	s.order = append(s.order, env.Operation)
	switch env.Operation {
	case protocol.OpRunsClaimMessage:
		var d protocol.RunMessageDelivery
		_ = json.Unmarshal(env.Payload, &d)
		s.m.RunMessageDelivery = d
		s.m.Status = protocol.RunMessageDispatching
	case protocol.OpRunsReportMessage:
		var p protocol.RunMessageReceipt
		_ = json.Unmarshal(env.Payload, &p)
		s.m.Status, s.m.Reason = p.Status, p.Reason
	}
	if s.after != nil {
		s.after(env.Operation)
	}
	if s.lose[env.Operation] > 0 {
		s.lose[env.Operation]--
		_, _ = w.Write([]byte("lost"))
		return
	}
	body, _ := json.Marshal(s.m)
	_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: body}})
}

func newMessageRunner(t *testing.T) (*Runner, *messageTransport, protocol.Assignment) {
	t.Helper()
	return newMessageRunnerAt(t, filepath.Join(t.TempDir(), "journal.db"))
}

func newMessageRunnerAt(t *testing.T, path string) (*Runner, *messageTransport, protocol.Assignment) {
	t.Helper()
	as := protocol.Assignment{RunID: "run_message", TaskID: "task_message", ActionID: "action_message", SpecVersion: 1, Generation: 1, RunToken: "run-token", Role: protocol.RoleImplement, LeaseID: "lease_message", RunMessages: true, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
	fake := &messageTransport{m: protocol.RunMessage{RunMessageDelivery: protocol.RunMessageDelivery{MessageID: "msg_message"}, RunID: as.RunID, TaskID: as.TaskID, ActionID: as.ActionID, SpecVersion: 1, Generation: 1, Text: "secret supplement", Status: protocol.RunMessageQueued}, calls: map[string][]protocol.Envelope{}, lose: map[string]int{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, `{"terminal":"terminal_original","session_id":"native_original"}`); err != nil {
		t.Fatal(err)
	}
	r := New(Config{ServerURL: srv.URL, HTTPClient: srv.Client(), Journal: j, IDs: testkit.NewIDGen(clock, 1), Now: clock.Now, RetryDelay: time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return r, fake, as
}

type messageWorker struct {
	calls  int
	status protocol.RunMessageStatus
	reason string
}

func (w *messageWorker) Events() <-chan harness.WorkerEvent  { return nil }
func (w *messageWorker) Wait() (harness.WorkerResult, error) { return harness.WorkerResult{}, nil }
func (w *messageWorker) MessageTarget() (string, string)     { return "thread_original", "turn_original" }
func (w *messageWorker) SendMessage(_ context.Context, text string) (protocol.RunMessageStatus, string) {
	w.calls++
	return w.status, w.reason
}

func TestRunMessageRetriesOnlyDurableServerRequests(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	fake.lose[protocol.OpRunsClaimMessage] = 1
	fake.lose[protocol.OpRunsReportMessage] = 1
	w := &messageWorker{status: protocol.RunMessageAccepted}
	if err := r.deliverRunMessage(t.Context(), as, w, fake.m, "receiver"); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Fatal("native invocation repeated")
	}
	for _, op := range []string{protocol.OpRunsClaimMessage, protocol.OpRunsReportMessage} {
		calls := fake.calls[op]
		if len(calls) != 2 || calls[0].RequestID != calls[1].RequestID || string(calls[0].Payload) != string(calls[1].Payload) {
			t.Fatal("server retry changed durable identity", calls)
		}
	}
	if d, err := r.cfg.Journal.MessageDelivery(t.Context(), as.RunID, fake.m.MessageID); err != nil || !d.Invoked || d.Report == nil {
		t.Fatal(d, err)
	}
	if err := r.recoverRunMessages(t.Context(), as); err != nil || w.calls != 1 {
		t.Fatal("restart repeated native input", err)
	}
}

func TestRunMessageRestartNeverRepeatsUnconfirmedNativeInvocation(t *testing.T) {
	for _, invoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-call", true: "after-intent"}[invoked], func(t *testing.T) {
			r, fake, as := newMessageRunner(t)
			binding := protocol.RunMessageDelivery{MessageID: fake.m.MessageID, Receiver: "old-receiver", ThreadID: "old-thread", TurnID: "old-turn"}
			env, err := r.newEnvelope(protocol.OpRunsClaimMessage, runTarget(as), binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.cfg.Journal.SaveMessageIntent(t.Context(), journal.MessageDelivery{MessageID: fake.m.MessageID, RunID: as.RunID, Claim: *env}); err != nil {
				t.Fatal(err)
			}
			if invoked {
				// The invoked bit is recorded only after a confirmed claim.
				fake.m.RunMessageDelivery, fake.m.Status = binding, protocol.RunMessageDispatching
				if err := r.cfg.Journal.MarkMessageInvoked(t.Context(), fake.m.MessageID); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.recoverRunMessages(t.Context(), as); err != nil {
				t.Fatal(err)
			}
			want := protocol.RunMessageFailed
			if invoked {
				want = protocol.RunMessageUnknown
			}
			if fake.m.Status != want || fake.m.Receiver != "old-receiver" {
				t.Fatal(fake.m)
			}
			before := len(fake.calls[protocol.OpRunsReportMessage])
			if err := r.recoverRunMessages(t.Context(), as); err != nil || len(fake.calls[protocol.OpRunsReportMessage]) != before {
				t.Fatal("recovery duplicated settled proof", err)
			}
		})
	}
}

func TestWorkerMessagesOfferOnceAndRequireOriginalReceiverAck(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	m := &workerMessages{r: r, as: as, receiver: "original-receiver"}
	first, err := m.receive(t.Context())
	if err != nil || first == nil || first.Text == "" {
		t.Fatal(first, err)
	}
	if fake.m.Status != protocol.RunMessageDispatching {
		t.Fatal("offer falsely acknowledged receipt")
	}
	second, err := m.receive(t.Context())
	if err != nil || second == nil || second.Text != "" {
		t.Fatal("message reoffered", second, err)
	}
	other := &workerMessages{r: r, as: as, receiver: "replacement-receiver"}
	if err := other.ack(t.Context(), first.MessageID); err == nil {
		t.Fatal("new worker acknowledged old delivery")
	}
	if err := m.ack(t.Context(), first.MessageID); err != nil {
		t.Fatal(err)
	}
	if fake.m.Status != protocol.RunMessageAccepted || fake.m.Reason != "worker_acknowledged" {
		t.Fatal(fake.m)
	}
	before := fake.calls[protocol.OpRunsReportMessage][0]
	if err := m.ack(t.Context(), first.MessageID); err != nil {
		t.Fatal(err)
	}
	if last := fake.calls[protocol.OpRunsReportMessage][1]; last.RequestID != before.RequestID {
		t.Fatal("ack retry changed identity")
	}
	if pending, err := m.receive(t.Context()); err != nil || pending != nil {
		t.Fatal(pending, err)
	}
}

func TestWorkerMessageReceiveRespectsPauseAndFence(t *testing.T) {
	for _, mode := range []string{"held", "fenced", "finished"} {
		t.Run(mode, func(t *testing.T) {
			r, fake, as := newMessageRunner(t)
			switch mode {
			case "held":
				_ = r.cfg.Journal.SetHeld(t.Context(), as.RunID, true)
			case "fenced":
				_ = r.cfg.Journal.SetFenced(t.Context(), as.RunID, true)
			case "finished":
				_ = r.cfg.Journal.SetPhase(t.Context(), as.RunID, journal.PhaseWorkerDone)
			}
			m := &workerMessages{r: r, as: as, receiver: "receiver"}
			if _, err := m.receive(t.Context()); err == nil || len(fake.calls[protocol.OpRunsClaimMessage]) != 0 {
				t.Fatal("controlled worker received a message", err)
			}
		})
	}
}

func TestWorkerMessageToolDoesNotExposeUnresolvedText(t *testing.T) {
	r := harness.Results{Spec: harness.WorkerSpec{ReceiveMessages: func(context.Context) (*protocol.RunMessage, error) {
		return &protocol.RunMessage{RunMessageDelivery: protocol.RunMessageDelivery{MessageID: "id"}, Status: protocol.RunMessageUnknown, Text: "DO-NOT-REPLAY"}, nil
	}, AckMessage: func(context.Context, string) error { return nil }}}
	res := r.Handle(t.Context(), harness.ToolCall{Tool: harness.ReceiveMessagesTool, Arguments: json.RawMessage(`{}`)})
	if !res.Success || strings.Contains(res.Text, "DO-NOT-REPLAY") {
		t.Fatal(res)
	}
}

func retainAcceptedMessage(t *testing.T, r *Runner, fake *messageTransport, as protocol.Assignment) protocol.Envelope {
	t.Helper()
	binding := protocol.RunMessageDelivery{MessageID: fake.m.MessageID, Receiver: "original-receiver", ThreadID: "original-thread", TurnID: "original-turn"}
	claim, err := r.newEnvelope(protocol.OpRunsClaimMessage, runTarget(as), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SaveMessageIntent(t.Context(), journal.MessageDelivery{MessageID: binding.MessageID, RunID: as.RunID, Claim: *claim}); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.MarkMessageInvoked(t.Context(), binding.MessageID); err != nil {
		t.Fatal(err)
	}
	fake.m.RunMessageDelivery, fake.m.Status = binding, protocol.RunMessageDispatching
	env, err := r.messageReceiptEnvelope(t.Context(), as, protocol.RunMessageReceipt{RunMessageDelivery: binding, Status: protocol.RunMessageAccepted})
	if err != nil {
		t.Fatal(err)
	}
	return *env
}

func TestRunMessageReceiptRecoveryInEveryFinalPhase(t *testing.T) {
	for _, phase := range []string{journal.PhaseWorkerDone, journal.PhaseSubmitting, journal.PhaseReleasing} {
		t.Run(phase, func(t *testing.T) {
			r, fake, as := newMessageRunner(t)
			original := retainAcceptedMessage(t, r, fake, as)
			workers := &completionWorkers{}
			r.cfg.Workers, r.cfg.Workspaces = workers, completionWorkspaces{}
			if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
				t.Fatal(err)
			}
			if phase == journal.PhaseWorkerDone {
				result, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "finished once"}}})
				if err := r.cfg.Journal.WorkerDone(t.Context(), as.RunID, result); err != nil {
					t.Fatal(err)
				}
			} else {
				op, payload := protocol.OpRunsSubmitArtifact, any(protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "finished once"}})
				if phase == journal.PhaseReleasing {
					op, payload = protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "cancelled"}
				}
				if err := r.finish(t.Context(), as, phase, op, payload); err != nil {
					t.Fatal(err)
				}
			}
			fake.lose[protocol.OpRunsReportMessage] = 1
			if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			if workers.starts != 0 || len(fake.calls[protocol.OpRunsClaimMessage]) != 0 || fake.m.Status != protocol.RunMessageAccepted {
				t.Fatal("recovery resumed execution or abandoned receipt", fake.calls, workers.starts)
			}
			if len(fake.order) != 3 || fake.order[0] != protocol.OpRunsReportMessage || fake.order[1] != protocol.OpRunsReportMessage {
				t.Fatal("receipt did not precede final report", fake.order)
			}
			for _, call := range fake.calls[protocol.OpRunsReportMessage] {
				if !reflect.DeepEqual(call, original) {
					t.Fatal("receipt retry changed original identity", call, original)
				}
			}
			if pending, err := r.cfg.Journal.PendingMessageDeliveries(t.Context(), as.RunID); err != nil || len(pending) != 0 {
				t.Fatal("available proof abandoned", pending, err)
			}
			if run, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID); err != nil || run.Phase != journal.PhaseDone {
				t.Fatal(run, err)
			}
		})
	}
}

func TestRunMessageReceiptSurvivesLostUploadAndRestartDuringCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	r, fake, as := newMessageRunnerAt(t, path)
	original := retainAcceptedMessage(t, r, fake, as)
	if err := r.finish(t.Context(), as, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "finished once"}}); err != nil {
		t.Fatal(err)
	}
	final, _, _ := r.cfg.Journal.RequestForRun(t.Context(), as.RunID)
	ctx, cancel := context.WithCancel(t.Context())
	fake.lose[protocol.OpRunsReportMessage] = 1
	fake.after = func(op string) {
		if op == protocol.OpRunsReportMessage {
			cancel()
		}
	}
	if err := r.report(ctx, as); err == nil {
		t.Fatal("lost upload incorrectly finalized run")
	}
	if pending, err := r.cfg.Journal.PendingMessageDeliveries(t.Context(), as.RunID); err != nil || len(pending) != 1 || !reflect.DeepEqual(*pending[0].Report, original) {
		t.Fatal("receipt was not retained", pending, err)
	}
	if len(fake.calls[protocol.OpRunsSubmitArtifact]) != 0 {
		t.Fatal("final report sent ahead of unresolved receipt")
	}
	fake.mu.Lock()
	fake.after = nil
	fake.mu.Unlock()
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(t.Context(), path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	cfg := r.cfg
	cfg.Journal, cfg.Workspaces = j, completionWorkspaces{}
	restarted := New(cfg)
	if err := restarted.report(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if calls := fake.calls[protocol.OpRunsSubmitArtifact]; len(calls) != 1 || !reflect.DeepEqual(calls[0], final.Envelope) {
		t.Fatal("completion request changed after restart", calls)
	}
	if calls := fake.calls[protocol.OpRunsReportMessage]; len(calls) != 2 || !reflect.DeepEqual(calls[1], original) || len(fake.calls[protocol.OpRunsClaimMessage]) != 0 {
		t.Fatal("recovery changed receipt or reclaimed message", calls)
	}
}

func TestRunMessageDoneReceiptRecoveryIsIndependentOfHelloAndWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	r, fake, as := newMessageRunnerAt(t, path)
	original := retainAcceptedMessage(t, r, fake, as)
	if err := r.cfg.Journal.Done(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(t.Context(), path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	cfg := r.cfg
	cfg.Journal = j
	restarted := New(cfg)
	if runs, err := j.Runs(t.Context()); err != nil || len(runs) != 0 {
		t.Fatal("done run would appear in hello", runs, err)
	}
	before, err := restarted.runtimeObservation(t.Context())
	if err != nil || before.PendingOperations != 1 {
		t.Fatal("done receipt disappeared from maintenance responsibility", before, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	fake.after = func(op string) {
		if op == protocol.OpRunsReportMessage {
			cancel()
		}
	}
	// Run only the independent lifecycle, with no Workers/hello/claim machinery.
	restarted.finalMessageReceipts(ctx)
	fake.mu.Lock()
	fake.after = nil
	fake.mu.Unlock()
	// A shutdown before settlement still replays the same saved receipt.
	if err := restarted.recoverFinalMessageReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatal("historical recovery performed an execution/control operation", fake.calls)
	}
	for _, call := range fake.calls[protocol.OpRunsReportMessage] {
		if !reflect.DeepEqual(call, original) {
			t.Fatal(call, original)
		}
	}
	if n, err := j.PendingMessageDeliveryCount(t.Context()); err != nil || n != 0 {
		t.Fatal("done receipt abandoned", n, err)
	}
	after, err := restarted.runtimeObservation(t.Context())
	if err != nil || after.PendingOperations != 0 {
		t.Fatal(after, err)
	}
}

func TestRunMessageReceiptSavePreservesFirstRequestIdentity(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	original := retainAcceptedMessage(t, r, fake, as)
	changed, err := r.messageReceiptEnvelope(t.Context(), as, protocol.RunMessageReceipt{RunMessageDelivery: fake.m.RunMessageDelivery, Status: protocol.RunMessageUnknown, Reason: "receiver_restarted"})
	if err != nil || !reflect.DeepEqual(*changed, original) {
		t.Fatal("recovery replaced available accepted evidence", changed, err)
	}
}

func TestWorkerMessageAckRequiresOriginalNativeIncarnation(t *testing.T) {
	for _, thread := range []string{
		`{"terminal":"terminal_original","session_id":"native_replacement"}`,
		`{"terminal":"terminal_replacement","session_id":"native_original"}`,
	} {
		t.Run(thread, func(t *testing.T) {
			r, fake, as := newMessageRunner(t)
			m := &workerMessages{r: r, as: as, receiver: "receiver"}
			msg, err := m.receive(t.Context())
			if err != nil || msg == nil {
				t.Fatal(msg, err)
			}
			if err := r.cfg.Journal.SetThread(t.Context(), as.RunID, thread); err != nil {
				t.Fatal(err)
			}
			if err := m.ack(t.Context(), msg.MessageID); err == nil {
				t.Fatal("replacement acknowledged original delivery")
			}
			if fake.m.Status != protocol.RunMessageUnknown || fake.m.ThreadID != "native_original" {
				t.Fatal(fake.m)
			}
			if got, err := m.receive(t.Context()); err != nil || got == nil || got.Text != "" {
				t.Fatal("old text reoffered", got, err)
			}
		})
	}
}

func TestWorkerMessageRetainedAckSurvivesNativeReplacement(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	m := &workerMessages{r: r, as: as, receiver: "receiver"}
	msg, err := m.receive(t.Context())
	if err != nil || msg == nil {
		t.Fatal(msg, err)
	}
	if err := m.ack(t.Context(), msg.MessageID); err != nil {
		t.Fatal(err)
	}
	original := fake.calls[protocol.OpRunsReportMessage][0]
	if err := r.cfg.Journal.SetThread(t.Context(), as.RunID, `{"terminal":"replacement","session_id":"replacement"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.ack(t.Context(), msg.MessageID); err != nil {
		t.Fatal(err)
	}
	last := fake.calls[protocol.OpRunsReportMessage][1]
	if last.RequestID != original.RequestID || fake.m.Status != protocol.RunMessageAccepted {
		t.Fatal("saved proof changed", last)
	}
}

func TestWorkerMessageWaitsForStableNativeIdentity(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	if err := r.cfg.Journal.SetThread(t.Context(), as.RunID, `{"terminal":"terminal_original"}`); err != nil {
		t.Fatal(err)
	}
	m := &workerMessages{r: r, as: as, receiver: "receiver"}
	if got, err := m.receive(t.Context()); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if len(fake.calls[protocol.OpRunsClaimMessage]) != 0 || fake.m.Status != protocol.RunMessageQueued {
		t.Fatal("claimed without stable identity")
	}
}

type messageTimeoutTransport struct {
	base     http.RoundTripper
	timedOut bool
}

func (tr *messageTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, err
		}
		if env.Operation == protocol.OpRunsReportMessage && !tr.timedOut {
			tr.timedOut = true
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
	}
	return tr.base.RoundTrip(req)
}

func TestRunMessageLoopRetriesReceiptTimeoutWhileWorkerIsActive(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	r.c.http.Transport = &messageTimeoutTransport{base: r.c.http.Transport}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.after = func(op string) {
		if op == protocol.OpRunsReportMessage {
			cancel()
		}
	}
	w := &messageWorker{status: protocol.RunMessageAccepted}
	r.runMessageLoop(ctx, as, w)
	if w.calls != 1 || fake.m.Status != protocol.RunMessageAccepted {
		t.Fatal("live loop did not recover saved receipt", w.calls, fake.m)
	}
	if len(fake.calls[protocol.OpRunsReportMessage]) != 1 {
		t.Fatal(fake.calls)
	}
	d, err := r.cfg.Journal.MessageDelivery(t.Context(), as.RunID, fake.m.MessageID)
	if err != nil || d.Report == nil || d.Report.RequestID != fake.calls[protocol.OpRunsReportMessage][0].RequestID {
		t.Fatal("recovery changed receipt identity", d, err)
	}
}

type messageCancelTransport struct {
	base      http.RoundTripper
	operation string
}

func (tr messageCancelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, err
		}
		if env.Operation == tr.operation {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
	}
	return tr.base.RoundTrip(req)
}
func TestWorkerMessageSameReceiverRecoversTimedOutToolCalls(t *testing.T) {
	for _, op := range []string{protocol.OpRunsClaimMessage, protocol.OpRunsReportMessage} {
		t.Run(op, func(t *testing.T) {
			r, fake, as := newMessageRunner(t)
			m := &workerMessages{r: r, as: as, receiver: "original-receiver"}
			base := r.c.http.Transport
			if op == protocol.OpRunsReportMessage {
				if msg, err := m.receive(t.Context()); err != nil || msg == nil {
					t.Fatal(msg, err)
				}
			}
			r.c.http.Transport = messageCancelTransport{base: base, operation: op}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			var err error
			if op == protocol.OpRunsClaimMessage {
				_, err = m.receive(ctx)
			} else {
				err = m.ack(ctx, fake.m.MessageID)
			}
			cancel()
			if err == nil {
				t.Fatal("expected timeout")
			}
			saved, err := r.cfg.Journal.MessageDelivery(t.Context(), as.RunID, fake.m.MessageID)
			if err != nil {
				t.Fatal(err)
			}
			r.c.http.Transport = base
			msg, err := m.receive(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if op == protocol.OpRunsClaimMessage {
				if msg == nil || msg.Text == "" || len(fake.calls[op]) != 1 || fake.calls[op][0].RequestID != saved.Claim.RequestID {
					t.Fatal("claim retry lost original identity", msg, fake.calls)
				}
			} else {
				if msg != nil || fake.m.Status != protocol.RunMessageAccepted || saved.Report == nil || fake.calls[op][0].RequestID != saved.Report.RequestID {
					t.Fatal("retained acknowledgement not recovered", msg, fake.m)
				}
			}
		})
	}
}

func TestStoppedWorkerClosesBeforeRetainedReceiptUpload(t *testing.T) {
	r, fake, as := newMessageRunner(t)
	r.cfg.Worker = protocol.WorkerOrca
	if err := r.cfg.Journal.BindUnstartedWorkerOrigin(t.Context(), as.RunID, protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{}, 1)
	r.cfg.Workers = &cancelCompletedWorkers{release: func(context.Context) error { closed <- struct{}{}; return nil }}
	retainAcceptedMessage(t, r, fake, as)
	body, _ := json.Marshal(harness.WorkerResult{})
	if err := r.cfg.Journal.WorkerDone(t.Context(), as.RunID, body); err != nil {
		t.Fatal(err)
	}
	r.c.http.Transport = messageCancelTransport{base: r.c.http.Transport, operation: protocol.OpRunsReportMessage}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runCtx, stop := context.WithCancelCause(ctx)
	stop(errStopped)
	a := &active{as: as, desired: protocol.DesiredStopped}
	done := make(chan error, 1)
	go func() { done <- r.execute(ctx, runCtx, as.RunID, a) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("receipt upload blocked original worker shutdown")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	d, err := r.cfg.Journal.MessageDelivery(t.Context(), as.RunID, fake.m.MessageID)
	if err != nil || d.Report == nil {
		t.Fatal("lost retained receipt", d, err)
	}
}
