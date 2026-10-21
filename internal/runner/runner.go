// Package runner is the client side of the runner protocol. A runner pulls
// work from tend, lays out workspaces, drives workers and reports their
// results.
//
// The runner writes to its journal before it acts: a claim is journaled
// before it is sent, an assignment before its workspace is touched, and a
// final report before it is sent. After a restart it resends journaled
// requests under their original IDs and reconciles the runs it holds
// through hello before it claims anything new.
//
// The runner fences itself. Every renewal sets a local deadline of the send
// time plus the lease TTL minus a margin; when a run's deadline passes, its
// worker is stopped before tend can consider the lease expired, and the run
// waits for reconciliation. Only the party that writes to the workspace can
// make a lease mean anything.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// Workspaces lays out and collects the workspaces of runs.
type Workspaces interface {
	// Prepare lays out an assignment's workspace. It is idempotent: a
	// resumed run calls it again to find its workspace. The report is what
	// an implement run tells tend about its mounts.
	Prepare(ctx context.Context, as protocol.Assignment) (Prepared, error)
	// Collect commits an implement run's changes and returns the artifact.
	// It returns ErrNoChanges when no mount changed.
	Collect(ctx context.Context, as protocol.Assignment) (protocol.ArtifactPayload, error)
	// Finish cleans up after a run, for example by removing an acceptance
	// workspace.
	Finish(ctx context.Context, as protocol.Assignment) error
}

// Prepared is a laid-out workspace.
type Prepared struct {
	Dir        string
	ScratchDir string
	Report     protocol.WorkspaceReport
	// Guide describes the workspace to a worker that starts inside a
	// repository, where tend writes no AGENTS.md (D37, D65).
	Guide string
}

// Workspace errors with their own release reasons.
var (
	// ErrNoChanges is returned by Collect when the worker changed nothing.
	ErrNoChanges = errors.New("runner: no changes")
	// ErrWorkspaceMoved is returned by Prepare when the workspace is not
	// as the artifact under acceptance recorded it.
	ErrWorkspaceMoved = errors.New("runner: the workspace is not as the artifact recorded")
	// ErrFetchFailed is returned by Prepare when a repository could not be
	// fetched; the runner never starts from a stale base instead.
	ErrFetchFailed = errors.New("runner: fetch failed")
	// ErrBaseNotFound is returned by Prepare when a mount's base does not
	// exist on the remote.
	ErrBaseNotFound = errors.New("runner: base not found")
)

// reasonFor names a workspace failure for tend.
func reasonFor(err error, fallback string) string {
	switch {
	case errors.Is(err, ErrNoChanges):
		return "no_changes"
	case errors.Is(err, ErrWorkspaceMoved):
		return "workspace_moved"
	case errors.Is(err, ErrFetchFailed):
		return "fetch_failed"
	case errors.Is(err, ErrBaseNotFound):
		return "base_not_found"
	}
	return fallback
}

// Config assembles a Runner.
type Config struct {
	ServerURL  string
	HostID     string
	HostToken  string
	Version    string
	HTTPClient *http.Client
	Journal    *journal.Journal
	// Worker is the kind of Workers, reported to tend (D64). Default
	// appserver.
	Worker     protocol.WorkerKind
	Workers    harness.Workers
	Workspaces Workspaces
	Now        func() time.Time
	IDs        protocol.IDGen
	Log        *slog.Logger
	// RetryDelay separates attempts to resend a request. Default 2s.
	RetryDelay time.Duration
	// EventFlush is how often run events are uploaded. Default 500ms.
	EventFlush time.Duration
	// RequestTimeout bounds each request to tend. It must exceed how long
	// tend holds a claim open waiting for work (25s). Default 60s.
	RequestTimeout time.Duration
}

// Why a worker is stopped, as the cause of its context. The harness's
// causes tell a worker whose agent outlives it what to do with the agent.
var (
	errFenced  = errors.New("runner: the lease could not be renewed in time")
	errStopped = fmt.Errorf("runner: %w", harness.ErrStopped)
	errPaused  = fmt.Errorf("runner: %w", harness.ErrPaused)
	errHeld    = fmt.Errorf("runner: %w", harness.ErrHeld)
)

// active is a run with a live executor.
type active struct {
	as       protocol.Assignment
	ctx      context.Context
	cancel   context.CancelCauseFunc
	deadline time.Time
	desired  protocol.RunDesired
	// heldAt is when the runner itself learned of a takeover; a desired
	// state tend sent before then does not undo it.
	heldAt time.Time
	resume chan struct{}
	// stopAttempt stops the current worker attempt, for example to pause
	// the run; nil while no worker runs.
	stopAttempt context.CancelCauseFunc
	// reporting marks a run that only sends its final report. It no
	// longer writes to the workspace, so it is not fenced.
	reporting bool
}

// Runner pulls and executes runs for one host.
type Runner struct {
	cfg       Config
	c         *client
	reconcile chan struct{}

	mu      sync.Mutex
	ttl     time.Duration
	ttlWake chan struct{} // closed when the TTL changes
	active  map[string]*active
	wg      sync.WaitGroup
}

// New returns a Runner.
func New(cfg Config) *Runner {
	if cfg.Worker == "" {
		cfg.Worker = protocol.WorkerAppServer
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if cfg.EventFlush <= 0 {
		cfg.EventFlush = 500 * time.Millisecond
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = time.Minute
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Runner{
		cfg: cfg,
		c: &client{base: cfg.ServerURL, http: cfg.HTTPClient, hostID: cfg.HostID, token: cfg.HostToken, log: cfg.Log, now: cfg.Now,
			timeout: cfg.RequestTimeout},
		reconcile: make(chan struct{}, 1),
		ttl:       time.Minute,
		ttlWake:   make(chan struct{}),
		active:    map[string]*active{},
	}
}

// Run recovers, reconciles and then claims and executes runs until ctx is
// cancelled. Executors stop with ctx; their runs stay in the journal and
// resume after the next start.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	// Deferred calls run in reverse: stop the goroutines, then join them.
	defer r.wg.Wait()
	defer cancel()
	// Lease upkeep runs before anything can start a worker.
	r.wg.Go(func() { r.heartbeats(ctx) })
	r.wg.Go(func() { r.watchdog(ctx) })
	r.wg.Go(func() { r.reconciler(ctx) })
	if err := r.resendClaims(ctx); err != nil {
		return err
	}
	if err := r.retry(ctx, func() error { return r.hello(ctx) }); err != nil {
		return err
	}
	r.claims(ctx)
	return ctx.Err()
}

func (r *Runner) newEnvelope(op string, target protocol.Target, payload any) (*protocol.Envelope, error) {
	env := &protocol.Envelope{RequestID: r.cfg.IDs.New(protocol.PrefixRequest), Operation: op, Target: target}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("runner: encode %s: %w", op, err)
		}
		env.Payload = b
	}
	return env, nil
}

// retry runs fn until it succeeds, fails for good, or ctx ends. Only
// transport failures are retried, always with the same request.
func (r *Runner) retry(ctx context.Context, fn func() error) error {
	for {
		err := fn()
		if err == nil || !isRetryable(err) {
			return err
		}
		if ctx.Err() != nil {
			// The runner is stopping; there is nothing to retry.
			return ctx.Err()
		}
		r.cfg.Log.WarnContext(ctx, "request failed, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.cfg.RetryDelay):
		}
	}
}

// send delivers env, resending it unchanged on transport failures.
func (r *Runner) send(ctx context.Context, cred credential, env *protocol.Envelope) (protocol.Operation, error) {
	var op protocol.Operation
	err := r.retry(ctx, func() error {
		var err error
		op, err = r.c.do(ctx, cred, env)
		return err
	})
	if err == nil && op.Status == protocol.OpFailed && op.Error != nil {
		return op, op.Error
	}
	return op, err
}

func (r *Runner) leaseTTL() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ttl
}

func (r *Runner) setTTL(ms int64) {
	if ms <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ttl := time.Duration(ms) * time.Millisecond; ttl != r.ttl {
		r.ttl = ttl
		close(r.ttlWake)
		r.ttlWake = make(chan struct{})
	}
}

// ttlChanged returns a channel closed by the next TTL change, so waits
// sized by the TTL can be recomputed.
func (r *Runner) ttlChanged() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ttlWake
}

// fenceDeadline is when a run must stop writing if its lease, renewed by a
// request sent at sent, is not renewed again.
func (r *Runner) fenceDeadline(sent time.Time) time.Time {
	ttl := r.leaseTTL()
	return sent.Add(ttl - ttl/4)
}

// resendClaims finishes claims a previous process sent but did not see
// answered: the same request ID returns the same assignment. Its lease may
// have lapsed meanwhile, so the run only starts once hello confirms it.
func (r *Runner) resendClaims(ctx context.Context) error {
	reqs, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	for _, req := range reqs {
		if req.Envelope.Operation != protocol.OpRunnerClaim {
			continue
		}
		env := req.Envelope
		if err := r.claimOnce(ctx, &env, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// claims long-polls for work until ctx ends.
func (r *Runner) claims(ctx context.Context) {
	for ctx.Err() == nil {
		env, err := r.newEnvelope(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.ClaimPayload{Worker: r.cfg.Worker})
		if err == nil {
			err = r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: *env})
		}
		if err == nil {
			// A reply that arrives late, or a resent claim answered from
			// the record, carries a lease that started no later than the
			// first send; count from there.
			err = r.claimOnce(ctx, env, r.cfg.Now())
		}
		if err != nil && ctx.Err() == nil {
			r.cfg.Log.ErrorContext(ctx, "claim failed", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(r.cfg.RetryDelay):
			}
			// A claim tend committed but the journal could not take on
			// is finished under its recorded ID and confirmed by hello.
			if err := r.resendClaims(ctx); err == nil {
				r.requestReconcile()
			}
		}
	}
}

// claimOnce sends a journaled claim and takes on its assignment, if any.
// The run starts with its lease counted from sentAt; a zero sentAt only
// journals it for hello to confirm.
func (r *Runner) claimOnce(ctx context.Context, env *protocol.Envelope, sentAt time.Time) error {
	op, err := r.send(ctx, r.c.host(), env)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Rejected for good: the claim had no effect.
		return errors.Join(err, r.cfg.Journal.DeleteRequest(ctx, env.RequestID))
	}
	var res struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &res); err != nil {
		return fmt.Errorf("runner: decode claim: %w", err)
	}
	if res.Assignment == nil {
		return r.cfg.Journal.DeleteRequest(ctx, env.RequestID)
	}
	if err := r.cfg.Journal.AcceptAssignment(ctx, *res.Assignment, env.RequestID); err != nil {
		return err
	}
	as := res.Assignment
	r.cfg.Log.InfoContext(ctx, "run claimed", "run_id", as.RunID, "task_id", as.TaskID, "role", as.Role, "generation", as.Generation)
	r.setTTL(res.Assignment.LeaseTTLMillis)
	if !sentAt.IsZero() {
		r.start(ctx, *res.Assignment, sentAt)
	}
	return nil
}

// hello reconciles every run the journal holds with tend.
func (r *Runner) hello(ctx context.Context) error {
	runs, err := r.cfg.Journal.Runs(ctx)
	if err != nil {
		return err
	}
	held := make([]protocol.HeldRun, 0, len(runs))
	byID := map[string]journal.Run{}
	for _, run := range runs {
		held = append(held, protocol.HeldRun{RunID: run.Assignment.RunID, LeaseID: run.Assignment.LeaseID, Observed: r.observed(run)})
		byID[run.Assignment.RunID] = run
	}
	env, err := r.newEnvelope(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.HelloPayload{Held: held, Version: r.cfg.Version, Worker: r.cfg.Worker})
	if err != nil {
		return err
	}
	sent := r.cfg.Now()
	op, err := r.send(ctx, r.c.host(), env)
	if err != nil {
		return err
	}
	var res protocol.HelloResult
	if err := json.Unmarshal(op.Result, &res); err != nil {
		return fmt.Errorf("runner: decode hello: %w", err)
	}
	r.setTTL(res.LeaseTTLMillis)
	for _, v := range res.Runs {
		run := byID[v.RunID]
		// What tend wants now, not what it wanted when it assigned the run.
		as := run.Assignment
		as.Desired = v.Desired
		switch {
		case v.Verdict == protocol.VerdictContinue:
			if err := r.cfg.Journal.SetFenced(ctx, v.RunID, false); err != nil {
				return err
			}
			r.start(ctx, as, sent)
		case run.Phase == journal.PhaseSubmitting || run.Phase == journal.PhaseReleasing:
			// The final report is a fact either way; let it go out.
			r.start(ctx, as, sent)
		default:
			r.cfg.Log.InfoContext(ctx, "tend stopped a held run", "run_id", v.RunID, "reason", v.Reason)
			if a := r.lookup(v.RunID); a != nil {
				a.cancel(errStopped)
				continue
			}
			if err := r.dropOrRelease(ctx, run, "stopped by tend: "+v.Reason); err != nil {
				return err
			}
		}
	}
	for _, as := range res.Reassigned {
		if err := r.cfg.Journal.AcceptAssignment(ctx, as, ""); err != nil {
			return err
		}
		r.start(ctx, as, sent)
	}
	return nil
}

// observed is what the runner can say about a held run.
func (r *Runner) observed(run journal.Run) protocol.RunObserved {
	switch {
	case run.Phase == journal.PhaseClaimed:
		return protocol.RunStarting
	case run.Fenced:
		return protocol.RunInterrupted
	}
	return protocol.RunRunning
}

// dropOrRelease ends a held run without an executor: it is released if
// tend may still accept its credential, and forgotten otherwise.
func (r *Runner) dropOrRelease(ctx context.Context, run journal.Run, reason string) error {
	if err := r.finish(ctx, run.Assignment, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}); err != nil {
		return err
	}
	r.start(ctx, run.Assignment, r.cfg.Now())
	return nil
}

func (r *Runner) lookup(runID string) *active {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[runID]
}

// start launches the executor of a journaled run unless one is running.
func (r *Runner) start(ctx context.Context, as protocol.Assignment, renewedAt time.Time) {
	runID := as.RunID
	deadline := r.fenceDeadline(renewedAt)
	if !r.cfg.Now().Before(deadline) {
		// The renewal is already too old to write on; reconcile first.
		r.cfg.Log.WarnContext(ctx, "lease too old to start a run; reconciling", "run_id", runID)
		r.requestReconcile()
		return
	}
	r.mu.Lock()
	if a, ok := r.active[runID]; ok {
		a.deadline = deadline
		r.mu.Unlock()
		if as.Desired != "" {
			r.applyDesired(ctx, a, as.Desired, renewedAt)
		}
		return
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	desired := as.Desired
	if desired == "" {
		desired = protocol.DesiredRun
	}
	a := &active{as: as, ctx: runCtx, cancel: cancel, deadline: deadline, desired: desired, resume: make(chan struct{}, 1)}
	r.active[runID] = a
	r.mu.Unlock()
	r.wg.Go(func() {
		defer func() {
			cancel(nil)
			r.mu.Lock()
			delete(r.active, runID)
			r.mu.Unlock()
		}()
		if err := r.execute(ctx, runCtx, runID, a); err != nil && ctx.Err() == nil {
			r.cfg.Log.ErrorContext(ctx, "run executor failed", "run_id", runID, "err", err)
		}
	})
}

// heartbeats renews the leases of active runs every quarter TTL.
func (r *Runner) heartbeats(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.ttlChanged():
			continue
		case <-time.After(r.leaseTTL() / 4):
		}
		r.heartbeat(ctx)
	}
}

// applyDesired acts on what tend wants a run to do, as tend saw it at
// observed: stop it, pause or hold its worker, or let a paused or held run
// go on.
func (r *Runner) applyDesired(ctx context.Context, a *active, desired protocol.RunDesired, observed time.Time) {
	r.mu.Lock()
	if desired != protocol.DesiredStopped && observed.Before(a.heldAt) {
		// Sent before the takeover this runner reported was recorded.
		r.mu.Unlock()
		return
	}
	prev := a.desired
	a.desired = desired
	stopAttempt := a.stopAttempt
	r.mu.Unlock()
	if desired == protocol.DesiredHeld && prev != desired {
		// Journaled as soon as it is seen: the owner may hand the run
		// back before the next worker starts, which must still say so.
		if err := r.cfg.Journal.SetHeld(context.WithoutCancel(ctx), a.as.RunID, true); err != nil {
			r.cfg.Log.ErrorContext(ctx, "could not journal the takeover", "run_id", a.as.RunID, "err", err)
		}
	}
	if desired != prev {
		// A run waiting for tend sees every change, such as a pause
		// becoming a takeover.
		select {
		case a.resume <- struct{}{}:
		default:
		}
	}
	switch {
	case desired == protocol.DesiredStopped:
		a.cancel(errStopped)
	case desired == protocol.DesiredPaused && stopAttempt != nil:
		stopAttempt(errPaused)
	case desired == protocol.DesiredHeld && stopAttempt != nil:
		stopAttempt(errHeld)
	}
}

func (r *Runner) heartbeat(ctx context.Context) {
	r.mu.Lock()
	refs := make([]protocol.LeaseRef, 0, len(r.active))
	for id, a := range r.active {
		refs = append(refs, protocol.LeaseRef{RunID: id, LeaseID: a.as.LeaseID})
	}
	r.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	env, err := r.newEnvelope(protocol.OpRunnerHeartbeat, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.HeartbeatPayload{Runs: refs})
	if err != nil {
		return
	}
	sent := r.cfg.Now()
	// A stalled request must not hold up the next renewal.
	hbCtx, cancel := context.WithTimeout(ctx, r.leaseTTL()/4)
	defer cancel()
	op, err := r.c.do(hbCtx, r.c.host(), env)
	if err != nil {
		// The watchdog fences runs whose deadline passes meanwhile.
		r.cfg.Log.WarnContext(ctx, "heartbeat failed", "err", err)
		return
	}
	var res protocol.HeartbeatResult
	if err := json.Unmarshal(op.Result, &res); err != nil {
		return
	}
	r.setTTL(res.LeaseTTLMillis)
	deadline := r.fenceDeadline(sent)
	for _, s := range res.Runs {
		a := r.lookup(s.RunID)
		if a == nil {
			continue
		}
		if !s.Renewed {
			if !r.isReporting(a) {
				r.fence(ctx, s.RunID, a)
			}
			continue
		}
		r.mu.Lock()
		a.deadline = deadline
		r.mu.Unlock()
		r.applyDesired(ctx, a, s.Desired, sent)
	}
}

// watchdog fences runs whose lease deadline passed.
func (r *Runner) watchdog(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.ttlChanged():
		case <-time.After(max(r.leaseTTL()/20, 10*time.Millisecond)):
		}
		now := r.cfg.Now()
		r.mu.Lock()
		var late []string
		for id, a := range r.active {
			if !a.reporting && now.After(a.deadline) {
				late = append(late, id)
			}
		}
		r.mu.Unlock()
		for _, id := range late {
			if a := r.lookup(id); a != nil {
				r.fence(ctx, id, a)
			}
		}
	}
}

// fence stops a run's worker because its lease cannot be relied on, and
// asks for reconciliation.
func (r *Runner) fence(ctx context.Context, runID string, a *active) {
	if context.Cause(a.ctx) == nil {
		r.cfg.Log.WarnContext(ctx, "fencing run", "run_id", runID)
	}
	a.cancel(errFenced)
	r.requestReconcile()
}

func (r *Runner) isReporting(a *active) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return a.reporting
}

func (r *Runner) requestReconcile() {
	select {
	case r.reconcile <- struct{}{}:
	default:
	}
}

// reconciler runs hello whenever a run was fenced, until it succeeds.
func (r *Runner) reconciler(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.reconcile:
		}
		// Let fenced executors record the fence before reporting.
		r.waitIdleFenced(ctx)
		if err := r.retry(ctx, func() error { return r.hello(ctx) }); err != nil && ctx.Err() == nil {
			r.cfg.Log.ErrorContext(ctx, "reconciliation failed", "err", err)
		}
	}
}

// waitIdleFenced waits until fenced executors have stopped and recorded the
// fence, so hello reports them as interrupted and can restart them.
func (r *Runner) waitIdleFenced(ctx context.Context) {
	for ctx.Err() == nil {
		r.mu.Lock()
		busy := false
		for _, a := range r.active {
			if !a.reporting && errors.Is(context.Cause(a.ctx), errFenced) {
				busy = true
				break
			}
		}
		r.mu.Unlock()
		if !busy {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}
