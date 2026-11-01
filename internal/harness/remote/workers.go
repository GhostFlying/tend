package remote

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/procx"
	"github.com/coder/websocket"
)

type WorkerOptions struct {
	FrontendWait time.Duration

	Bin    string
	Helper string
	Orca   *orca.Client
	Model  string
	Effort string
	Log    *slog.Logger
}
type Workers struct {
	opts WorkerOptions
	mu   sync.Mutex
	runs map[string]*execution
}
type workerState struct {
	StateDir        string `json:"state_dir"`
	ProviderID      string `json:"provider_id"`
	Kind            string `json:"kind"`
	RunID           string `json:"run_id"`
	Cwd             string `json:"cwd"`
	Thread          string `json:"thread_id"`
	Terminal        string `json:"terminal,omitempty"`
	LaunchPending   bool   `json:"launch_pending,omitempty"`
	ExecutorStopped bool   `json:"executor_stopped,omitempty"`
	BriefRecorded   bool   `json:"brief_recorded,omitempty"`
	BriefPending    bool   `json:"brief_pending,omitempty"`
	Reconcile       bool   `json:"reconcile_required,omitempty"`
}
type execution struct {
	mu            sync.Mutex
	persistMu     sync.Mutex
	frontendReady chan struct{}
	frontendOnce  sync.Once
	ownerHeld     bool
	state         workerState
	spec          harness.WorkerSpec
	session       *traex.Session
	thread        harness.Thread
	ctx           context.Context
	stop          context.CancelFunc
	executor      *exec.Cmd
	executorDone  chan struct{}
	closed        chan struct{}
	closeOnce     sync.Once
	gateway       *http.Server
	listener      net.Listener
	token         string
	stateFile     string
	drainCancel   context.CancelFunc
	drainDone     chan struct{}
}

func NewWorkers(opts WorkerOptions) *Workers {
	bin := opts.Bin
	if bin == "" {
		bin = "traex"
	}
	if resolved, err := exec.LookPath(bin); err == nil {
		opts.Bin = resolved
	}
	if opts.FrontendWait == 0 {
		opts.FrontendWait = 2 * time.Minute
	}
	return &Workers{opts: opts, runs: map[string]*execution{}}
}
func (ws *Workers) Start(ctx context.Context, spec harness.WorkerSpec) (worker harness.Worker, err error) {
	defer func() { err = startupError(ctx, err) }()
	if spec.NativeDial == nil || spec.Lifetime == nil {
		return nil, fmt.Errorf("%w: native run transport unavailable", harness.ErrWorkerNotStarted)
	}
	ws.mu.Lock()
	ex := ws.runs[spec.RunID]
	ws.mu.Unlock()
	if ex != nil {
		select {
		case <-ex.closed:
			ex = nil
		default:
		}
	}
	if ex == nil {
		if spec.Thread != "" {
			var pin workerState
			if json.Unmarshal([]byte(spec.Thread), &pin) != nil || !pin.ExecutorStopped || pin.Reconcile || pin.BriefPending {
				return nil, errors.New("original native execution requires reconciliation after restart")
			}
		}
		var err error
		ex, err = ws.open(ctx, spec)
		if err != nil {
			return nil, err
		}
		ws.mu.Lock()
		ws.runs[spec.RunID] = ex
		ws.mu.Unlock()
	}
	ex.mu.Lock()
	if ex.state.RunID != spec.RunID || ex.state.Cwd != spec.Dir || ex.state.ProviderID != spec.ProviderID || ex.state.StateDir != spec.StateDir {
		ex.mu.Unlock()
		return nil, errors.New("original native execution binding differs")
	}
	ex.ownerHeld = false
	threadID, stopped := ex.state.Thread, ex.state.ExecutorStopped
	ex.mu.Unlock()
	defer func() {
		if err == nil {
			return
		}
		err = startupError(ctx, err)
		select {
		case <-ex.closed:
			return
		default:
		}
		ex.mu.Lock()
		held := ex.ownerHeld
		ex.mu.Unlock()
		if held || errors.Is(err, harness.ErrHeld) {
			err = errors.Join(err, harness.ErrHeld)
			ex.startDrain()
		} else if errors.Is(err, harness.ErrPaused) {
			ex.confirmPaused(false)
		}
	}()
	if threadID != "" && !stopped {
		for {
			idle, err := ex.session.ThreadIdle(ctx, threadID)
			if err != nil {
				return nil, err
			}
			if idle {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}

	ex.mu.Lock()
	if ex.state.RunID != spec.RunID || ex.state.Cwd != spec.Dir || ex.state.ProviderID != spec.ProviderID {
		ex.mu.Unlock()
		return nil, errors.New("original native execution binding differs")
	}
	drainCancel, drainDone := ex.drainCancel, ex.drainDone
	ex.drainCancel = nil
	ex.drainDone = nil
	ex.mu.Unlock()
	if drainCancel != nil {
		drainCancel()
		<-drainDone
	}
	select {
	case <-ex.closed:
		return nil, errors.New("native execution stopped; reconciliation is required")
	default:
	}
	ex.mu.Lock()
	if ex.ownerHeld {
		ex.mu.Unlock()
		return nil, harness.ErrHeld
	}
	ex.spec = spec
	nativeThread := ex.state.Thread
	reuseUnstarted := !ex.state.BriefRecorded
	ex.mu.Unlock()
	spec.Thread = nativeThread
	spec.OnThread = func(id string) error {
		ex.mu.Lock()
		ex.state.Thread = id
		ex.state.ExecutorStopped = false
		state := ex.state
		ex.mu.Unlock()
		return ex.save(state)
	}
	inner := traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Log: ws.opts.Log}, Model: ws.opts.Model, Effort: ws.opts.Effort, EnvironmentID: "remote", StrictResume: true, ReuseUnstarted: reuseUnstarted,
		Acquire: func(context.Context, harness.WorkerSpec) (*traex.Session, func(error), error) {
			return ex.session, func(cause error) {
				cause = errors.Join(cause, controlCause(cause))
				if control := context.Cause(ctx); errors.Is(control, harness.ErrHeld) || errors.Is(control, harness.ErrPaused) {
					cause = errors.Join(cause, control)
				}
				ex.mu.Lock()
				ownerHeld := ex.ownerHeld
				ex.mu.Unlock()
				if ownerHeld || errors.Is(cause, harness.ErrHeld) {
					cause = harness.ErrHeld
				}
				if errors.Is(cause, harness.ErrHeld) || errors.Is(cause, harness.ErrPaused) {
					if errors.Is(cause, harness.ErrHeld) {
						ex.mu.Lock()
						ex.ownerHeld = true
						ex.mu.Unlock()
					}
					if errors.Is(cause, harness.ErrPaused) && !ex.confirmPaused(errors.Is(cause, traex.ErrInterruptUnconfirmed)) {
						return
					}
					ex.startDrain()
					return
				}
				ex.close()
			}, nil
		},
		BeforeStart: func(ctx context.Context, in harness.WorkerSpec, th harness.Thread, input string) (string, error) {
			ex.mu.Lock()
			ex.thread = th
			state := ex.state
			ex.mu.Unlock()
			if state.BriefPending {
				return "", errors.New("original task context delivery requires reconciliation")
			}
			if !state.BriefRecorded {
				state.BriefPending = true
				if err := ex.save(state); err != nil {
					return "", err
				}
				_, brief := harness.Brief(in)
				if err := ex.session.InjectUser(ctx, th.ID(), brief); err != nil {
					if errors.Is(err, harness.ErrRejected) {
						state.BriefPending = false
						if saveErr := ex.save(state); saveErr != nil {
							return "", saveErr
						}
					}
					return "", err
				}
				state.BriefRecorded = true
				state.BriefPending = false
				if err := ex.save(state); err != nil {
					return "", err
				}
			}
			if in.Thread == "" {
				input = "The original Tend task brief is recorded above. Begin the task and report its result through the provided tools."
			}
			if err := ws.attachTUI(ctx, ex); err != nil {
				return "", err
			}
			if ws.opts.FrontendWait < 0 {
				return input, nil
			}
			timer := time.NewTimer(ws.opts.FrontendWait)
			defer timer.Stop()
			select {
			case <-ex.frontendReady:
				return input, nil
			case <-ctx.Done():
				return "", ctx.Err()
			case <-timer.C:
				return "", errors.New("native terminal did not confirm the original session")
			}

		},
	})
	worker, err = inner.Start(ctx, spec)
	if err != nil {
		return nil, errors.Join(err, controlCause(err))
	}
	return worker, nil
}

// confirmPaused retains a paused executor only after native idle confirmation.
// A refused or uncertain interrupt fences execution before reporting pause.
func (ex *execution) confirmPaused(interruptFailed bool) bool {
	if !interruptFailed {
		ctx, cancel := context.WithTimeout(ex.ctx, time.Second)
		defer cancel()
		ex.mu.Lock()
		thread := ex.state.Thread
		ex.mu.Unlock()
		for {
			ex.mu.Lock()
			held := ex.ownerHeld
			ex.mu.Unlock()
			if held {
				return true
			}
			idle, err := ex.session.ThreadIdle(ctx, thread)
			if err != nil {
				break
			}
			if idle {
				return true
			}
			select {
			case <-ctx.Done():
				interruptFailed = true
			case <-time.After(50 * time.Millisecond):
			}
			if interruptFailed {
				break
			}
		}
	}
	ex.mu.Lock()
	if ex.ownerHeld {
		ex.mu.Unlock()
		return true
	}
	ex.state.Reconcile = true
	ex.mu.Unlock()
	ex.close()
	return false
}
func (ws *Workers) open(ctx context.Context, spec harness.WorkerSpec) (*execution, error) {
	var state workerState
	if spec.Thread != "" {
		if json.Unmarshal([]byte(spec.Thread), &state) != nil || state.Kind != "central-traex" || state.RunID != spec.RunID || state.Cwd != spec.Dir || state.ProviderID != spec.ProviderID || state.StateDir != spec.StateDir || state.Thread == "" {
			return nil, errors.New("original native execution binding is unavailable")
		}
	} else {
		state = workerState{Kind: "central-traex", RunID: spec.RunID, Cwd: spec.Dir, ProviderID: spec.ProviderID, StateDir: spec.StateDir}
	}
	lifetime, stop := context.WithCancel(spec.Lifetime)
	ex := &execution{state: state, spec: spec, ctx: lifetime, stop: stop, closed: make(chan struct{}), frontendReady: make(chan struct{}), executorDone: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			ex.close()
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	_ = listener.Close()
	bin := ws.opts.Bin
	if bin == "" {
		bin = "traex"
	}
	cmd := exec.CommandContext(lifetime, bin, "exec-server", "--listen", "ws://"+address)
	cmd.Dir = spec.Dir
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "ORCA_") && key != "TEND_SCRATCH" && key != "TEND_ADDON" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "TEND_SCRATCH="+spec.ScratchDir, "TEND_ADDON=off")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	procx.Contain(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("native executor: %w", err)
	}
	ex.executor = cmd
	go func() { _ = cmd.Wait(); close(ex.executorDone); stop() }()
	var local *websocket.Conn
	for {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		local, _, err = websocket.Dial(probeCtx, "ws://"+address, nil)
		cancel()
		if err == nil {
			break
		}
		select {
		case <-lifetime.Done():
			return nil, errors.New("native executor exited")
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	local.SetReadLimit(32 << 20)
	tunnel, err := spec.NativeDial(ctx, "exec")
	if err != nil {
		_ = local.CloseNow()
		return nil, err
	}
	hello, _ := json.Marshal(Hello{Cwd: spec.Dir, ThreadID: state.Thread})
	if _, err = tunnel.Write(append(hello, '\n')); err != nil {
		_ = tunnel.Close()
		_ = local.CloseNow()
		return nil, err
	}
	stopHandshake := context.AfterFunc(ctx, func() { _ = tunnel.Close() })
	reader := bufio.NewReader(tunnel)
	line, err := reader.ReadBytes('\n')
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err != nil || json.Unmarshal(line, &ready) != nil || !ready.Ready {
		_ = tunnel.Close()
		_ = local.CloseNow()
		return nil, errors.New("central native execution unavailable")
	}
	stopHandshake()
	// Both directions are owned by this execution, including while held.
	go func() {
		defer stop()
		defer tunnel.Close()
		defer local.CloseNow()
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			if local.Write(lifetime, websocket.MessageText, line[:len(line)-1]) != nil {
				return
			}
		}
	}()
	go func() {
		defer stop()
		defer tunnel.Close()
		defer local.CloseNow()
		for {
			_, data, err := local.Read(lifetime)
			if err != nil {
				return
			}
			if _, err = tunnel.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()
	transport, err := spec.NativeDial(ctx, "control")
	if err != nil {
		return nil, err
	}
	session, err := traex.Connect(ctx, transport, "dev", ws.opts.Log)
	if err != nil {
		return nil, err
	}
	ex.session = session
	go func() {
		select {
		case <-session.Done():
			stop()
		case <-lifetime.Done():
		}
	}()
	if err := ws.gateway(ex); err != nil {
		return nil, err
	}
	go func() { <-lifetime.Done(); ex.close() }()
	ok = true
	return ex, nil
}
func (ex *execution) startDrain() {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if ex.thread == nil || ex.drainCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ex.ctx)
	done := make(chan struct{})
	ex.drainCancel = cancel
	ex.drainDone = done
	th := ex.thread
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-th.Events():
				if !ok {
					return
				}
				if ev.RequestUsage != nil {
					ex.mu.Lock()
					persist := ex.spec.OnRetainedEvent
					ex.mu.Unlock()
					if persist != nil {
						body, err := json.Marshal(ev.RequestUsage)
						if err != nil || persist(ex.ctx, harness.WorkerEvent{NativeID: ev.RequestUsage.RequestID, Kind: "usage.request", Body: body}) != nil {
							ex.stop()
							return
						}
					}
				}
			}
		}
	}()
}
func (ex *execution) close() {
	ex.closeOnce.Do(func() {
		ex.stop()
		if ex.listener != nil {
			_ = ex.listener.Close()
		}
		if ex.gateway != nil {
			_ = ex.gateway.Close()
		}
		if ex.session != nil {
			_ = ex.session.Close()
		}
		if ex.executor != nil {
			procx.KillGroup(ex.executor)
			<-ex.executorDone
		}
		ex.mu.Lock()
		state := ex.state
		state.ExecutorStopped = true
		ex.mu.Unlock()
		if state.Thread != "" {
			_ = ex.save(state)
		}
		close(ex.closed)
	})
}
func (ws *Workers) Release(ctx context.Context, runID, thread string) error {
	ws.mu.Lock()
	ex := ws.runs[runID]
	ws.mu.Unlock()
	if ex == nil {
		if thread == "" {
			return nil
		}
		var pin workerState
		if json.Unmarshal([]byte(thread), &pin) == nil && pin.Kind == "central-traex" && pin.RunID == runID && pin.ExecutorStopped && pin.StateDir != "" {
			return writePrivate(filepath.Join(pin.StateDir, "central-tui.json"), PrivateTUI{Closed: true, Thread: pin.Thread, Cwd: pin.Cwd})
		}
		return errors.New("original native execution stop must be reconciled")
	}
	ex.close()
	ex.mu.Lock()
	state := ex.state
	ex.mu.Unlock()
	if state.Thread != "" {
		if err := writePrivate(ex.stateFile, PrivateTUI{Closed: true, Thread: state.Thread, Cwd: state.Cwd}); err != nil {
			return err
		}
	}

	ws.mu.Lock()
	if ws.runs[runID] == ex {
		delete(ws.runs, runID)
	}
	ws.mu.Unlock()

	return nil
}
func (ws *Workers) ReleaseCompleted(ctx context.Context, runID, thread string, _ int64, _ bool) error {
	return ws.Release(ctx, runID, thread)
}
func (ws *Workers) ReleaseFinalizing(ctx context.Context, runID, thread string, _ int64, _ bool, _ func(string) error) error {
	return ws.Release(ctx, runID, thread)
}
func (ws *Workers) ReleaseHistorical(ctx context.Context, runID, thread string, held bool, _ int64) error {
	if held {
		ws.mu.Lock()
		ex := ws.runs[runID]
		ws.mu.Unlock()
		if ex != nil {
			select {
			case <-ex.closed:
				return ws.Release(ctx, runID, thread)
			default:
			}
		} else {
			var pin workerState
			if json.Unmarshal([]byte(thread), &pin) == nil && pin.Kind == "central-traex" && pin.RunID == runID && pin.ExecutorStopped && pin.StateDir != "" {
				return ws.Release(ctx, runID, thread)
			}
		}
		return harness.ErrHeld
	}
	return ws.Release(ctx, runID, thread)
}
func (ws *Workers) Close() {
	ws.mu.Lock()
	runs := make([]*execution, 0, len(ws.runs))
	for _, ex := range ws.runs {
		runs = append(runs, ex)
	}
	ws.mu.Unlock()
	for _, ex := range runs {
		ex.close()
	}
}

// PrivateTUI is a local capability file, never part of public inventory or argv.
type PrivateTUI struct {
	Model  string `json:"model,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	URL    string `json:"url"`
	Token  string `json:"token"`
	Thread string `json:"thread"`
	Cwd    string `json:"cwd"`
	Bin    string `json:"bin"`
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func writePrivate(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".native-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// NativeLiveMessages identifies direct native steering, rather than MCP polling.
func (*Workers) NativeLiveMessages() {}
