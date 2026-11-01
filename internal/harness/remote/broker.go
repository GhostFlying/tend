package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/coder/websocket"
)

// BrokerConfig enables center-side native processes, using the center user's home.
type BrokerConfig struct {
	Bin  string `json:"traex_bin"`
	Home string `json:"traex_home,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
	// Args supplies native configuration to isolated integration fixtures.
	Args []string `json:"-"`
}

// Authorize rechecks the original credential and the live lease/control state.
type Authorize func(context.Context) (protocol.RunDesired, error)
type Broker struct {
	cfg    BrokerConfig
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	runs   map[string]*nativeRun
	wg     sync.WaitGroup
}
type nativeRun struct {
	ctx             context.Context
	cancel          context.CancelFunc
	ready           chan struct{}
	mu              sync.Mutex
	appURL          string
	thread          string
	cwd             string
	exec            *websocket.Conn
	controlAttached bool
	startPending    bool
	startID         string
	execAttached    bool
	tuiResumed      bool
}

func NewBroker(ctx context.Context, cfg BrokerConfig, log *slog.Logger) *Broker {
	ctx, cancel := context.WithCancel(ctx)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Broker{cfg: cfg, log: log, ctx: ctx, cancel: cancel, runs: map[string]*nativeRun{}}
}
func (b *Broker) Close() {
	b.cancel()
	b.mu.Lock()
	for _, r := range b.runs {
		r.cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

// Serve handles a native transport only after the outer API authenticates its run.
func (b *Broker) Serve(w http.ResponseWriter, r *http.Request, runID, channel string, authorize Authorize) {
	if _, err := authorize(r.Context()); err != nil {
		http.Error(w, "native execution is not authorized", http.StatusForbidden)
		return
	}
	if channel == "exec" {
		b.serveExec(w, r, runID, authorize)
		return
	}
	if channel != "control" && channel != "tui" {
		http.NotFound(w, r)
		return
	}
	b.mu.Lock()
	run := b.runs[runID]
	b.mu.Unlock()
	if run == nil {
		http.Error(w, "native execution is unavailable", http.StatusConflict)
		return
	}
	select {
	case <-run.ready:
	case <-run.ctx.Done():
		http.Error(w, "native execution stopped", http.StatusConflict)
		return
	case <-r.Context().Done():
		return
	}
	if channel == "control" {
		run.mu.Lock()
		if run.controlAttached {
			run.mu.Unlock()
			http.Error(w, "native control already attached", http.StatusConflict)
			return
		}
		run.controlAttached = true
		run.mu.Unlock()
		defer func() { run.mu.Lock(); run.controlAttached = false; run.mu.Unlock() }()
	}

	client, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer client.CloseNow()
	client.SetReadLimit(32 << 20)
	upstream, _, err := websocket.Dial(run.ctx, run.appURL, nil)
	if err != nil {
		return
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(32 << 20)
	bridgeCtx, cancel := context.WithCancel(run.ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		defer client.CloseNow()
		for {
			kind, data, err := upstream.Read(bridgeCtx)
			if err != nil {
				return
			}
			var frame nativeFrame
			if json.Unmarshal(data, &frame) != nil {
				return
			}
			if channel == "tui" && frame.Method == "item/tool/call" {
				continue
			}
			if channel == "control" {
				run.mu.Lock()
				if run.startPending && string(frame.ID) == run.startID {
					var result struct {
						Thread struct {
							ID string `json:"id"`
						} `json:"thread"`
					}
					_ = json.Unmarshal(frame.Result, &result)
					if result.Thread.ID == "" && frame.Error == nil || b.saveBinding(runID, nativeBinding{Cwd: run.cwd, Thread: result.Thread.ID}) != nil {
						run.mu.Unlock()
						run.cancel()
						return
					}
					run.thread = result.Thread.ID
					run.startPending = false
				}
				run.mu.Unlock()
			}
			if frame.Result != nil {
				var result struct {
					Thread struct {
						ID string `json:"id"`
					} `json:"thread"`
				}
				if json.Unmarshal(frame.Result, &result) == nil && result.Thread.ID != "" {
					run.mu.Lock()
					if channel == "control" {
						if run.thread == "" {
							if string(frame.ID) != run.startID {
								run.mu.Unlock()
								run.cancel()
								return
							}
							run.thread = result.Thread.ID
						} else if run.thread != result.Thread.ID {
							run.mu.Unlock()
							run.cancel()
							return
						}
					}
					if channel == "tui" && run.thread == result.Thread.ID {
						run.tuiResumed = true
					}
					run.mu.Unlock()
				}
			}
			if err := client.Write(bridgeCtx, kind, data); err != nil {
				return
			}
		}
	}()
	for {
		kind, data, err := client.Read(bridgeCtx)
		if err != nil {
			break
		}
		control, err := authorize(bridgeCtx)
		if err != nil {
			run.cancel()
			break
		}
		if channel == "control" {
			normalized, checkErr := constrainControl(data, run, control)
			if checkErr != nil {
				var frame nativeFrame
				_ = json.Unmarshal(data, &frame)
				_ = writeFrame(bridgeCtx, client, map[string]any{"id": frame.ID, "error": map[string]any{"code": -32600, "message": "native request is outside this run's session or execution settings", "data": map[string]any{"desired": control}}})
				continue
			}
			data = normalized
			var frame nativeFrame
			_ = json.Unmarshal(data, &frame)
			if frame.Method == "thread/start" && b.saveBinding(runID, nativeBinding{Cwd: run.cwd, Pending: true}) != nil {
				run.cancel()
				break
			}
		}

		if channel == "tui" {
			run.mu.Lock()
			thread, cwd := run.thread, run.cwd
			run.mu.Unlock()
			data, err = constrainTUI(data, thread, cwd)
			if err != nil {
				break
			}
			forward, err := checkTUI(data, thread, cwd, func() bool { state, e := authorize(bridgeCtx); return e == nil && state == protocol.DesiredHeld })
			if err != nil {
				var f nativeFrame
				_ = json.Unmarshal(data, &f)
				_ = writeFrame(bridgeCtx, client, map[string]any{"id": f.ID, "error": map[string]any{"code": -32600, "message": "native session control is unavailable"}})
				continue
			}
			if !forward {
				continue
			}
		}
		if err := upstream.Write(bridgeCtx, kind, data); err != nil {
			break
		}
	}
	cancel()
	_ = upstream.CloseNow()
	<-done
}
func (b *Broker) serveExec(w http.ResponseWriter, r *http.Request, runID string, authorize Authorize) {
	b.mu.Lock()
	if b.ctx.Err() != nil || b.runs[runID] != nil {
		b.mu.Unlock()
		http.Error(w, "native execution already exists", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	run := &nativeRun{ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	b.runs[runID] = run
	b.wg.Add(1)
	b.mu.Unlock()
	defer b.wg.Done()
	defer func() { cancel(); b.mu.Lock(); delete(b.runs, runID); b.mu.Unlock() }()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := authorize(ctx); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-monitorDone }()

	client, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer client.CloseNow()
	client.SetReadLimit(32 << 20)
	helloCtx, helloCancel := context.WithTimeout(ctx, 15*time.Second)
	_, data, err := client.Read(helloCtx)
	helloCancel()
	var hello Hello
	if err != nil || json.Unmarshal(data, &hello) != nil || strings.TrimSpace(hello.Cwd) == "" {
		return
	}
	if err := b.bindHello(runID, hello); err != nil {
		return
	}
	run.cwd = hello.Cwd
	run.thread = hello.ThreadID
	run.exec = client
	type packet struct {
		kind websocket.MessageType
		data []byte
	}
	incoming := make(chan packet)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		for {
			kind, data, err := client.Read(ctx)
			if err != nil {
				return
			}
			select {
			case incoming <- packet{kind: kind, data: data}:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = client.CloseNow(); <-readerDone }()
	tunnelReady := make(chan struct{})

	execListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	defer execListener.Close()
	execServer := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run.mu.Lock()
		if run.execAttached {
			run.mu.Unlock()
			http.Error(w, "executor already attached", http.StatusConflict)
			return
		}
		run.execAttached = true
		run.mu.Unlock()
		native, err := websocket.Accept(w, r, nil)
		if err != nil {
			cancel()
			return
		}
		defer native.CloseNow()
		native.SetReadLimit(32 << 20)
		select {
		case <-tunnelReady:
		case <-ctx.Done():
			return
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer cancel()
			defer native.CloseNow()
			select {
			case <-tunnelReady:
			case <-ctx.Done():
				return
			}
			for {
				select {
				case <-ctx.Done():
					return
				case frame := <-incoming:
					if native.Write(ctx, frame.kind, frame.data) != nil {
						return
					}
				}
			}
		}()
		copyFrames(ctx, native, client)
		cancel()
		_ = client.CloseNow()
		<-done
	})}
	go func() { _ = execServer.Serve(execListener) }()
	defer execServer.Close()
	if writeFrame(ctx, client, map[string]bool{"ready": true}) != nil {
		return
	}
	close(tunnelReady)

	appListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	appURL := "ws://" + appListener.Addr().String()
	_ = appListener.Close()
	bin := b.cfg.Bin
	if bin == "" {
		bin = "traex"
	}
	args := append([]string{}, b.cfg.Args...)
	args = append(args, "app-server", "--listen", appURL)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = b.cfg.Cwd
	cmd.Env = centerEnv(os.Environ(), b.cfg.Home, "ws://"+execListener.Addr().String())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	procx.Contain(cmd)
	if err := cmd.Start(); err != nil {
		b.log.WarnContext(ctx, "central native process unavailable", "run_id", runID)
		return
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); cancel() }()
	defer func() { cancel(); procx.KillGroup(cmd); <-exited }()
	startupCtx, stopStartup := context.WithTimeout(ctx, 30*time.Second)
	defer stopStartup()
	// A native handshake is a readiness probe; no thread or model turn is started.
	for {
		probeCtx, stop := context.WithTimeout(startupCtx, time.Second)
		probe, _, err := websocket.Dial(probeCtx, appURL, nil)
		stop()
		if err == nil {
			_ = probe.CloseNow()
			break
		}
		select {
		case <-startupCtx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	run.appURL = appURL
	close(run.ready)

	<-ctx.Done()
}
func centerEnv(inherited []string, home, executor string) []string {
	out := []string{}
	for _, e := range inherited {
		k, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "ORCA_") || upper == "TRAECLI_EXEC_SERVER_URL" || upper == "CODEX_EXEC_SERVER_URL" || upper == "TEND_ADDON" || upper == "TEND_SCRATCH" || home != "" && (upper == "TRAE_HOME" || upper == "TRAECLI_HOME") {
			continue
		}
		out = append(out, e)
	}
	if home != "" {
		out = append(out, "TRAE_HOME="+home, "TRAECLI_HOME="+home)
	}
	return append(out, "TRAECLI_EXEC_SERVER_URL="+executor, "TEND_ADDON=off")
}
func copyFrames(ctx context.Context, src, dst *websocket.Conn) {
	for {
		kind, b, err := src.Read(ctx)
		if err != nil {
			return
		}
		if dst.Write(ctx, kind, b) != nil {
			return
		}
	}
}

type nativeFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

var errTUIControl = errors.New("native TUI control is unavailable")

func checkTUI(data []byte, thread, cwd string, held func() bool) (bool, error) {
	var f nativeFrame
	if json.Unmarshal(data, &f) != nil {
		return false, errTUIControl
	}
	if f.Method == "" {
		return false, nil
	}
	var p struct {
		ThreadID string `json:"threadId"`
		Cwd      string `json:"cwd"`
	}
	if len(f.Params) > 0 && json.Unmarshal(f.Params, &p) != nil {
		return false, errTUIControl
	}
	if p.ThreadID != "" && (thread == "" || p.ThreadID != thread) {
		return false, errTUIControl
	}
	switch f.Method {
	case "initialize", "initialized", "account/read", "account/rateLimits/read", "model/list", "collaborationMode/list", "skills/list", "hooks/list", "app/list", "plugin/list", "mcpServerStatus/list", "experimentalFeature/list":
		return true, nil
	case "thread/resume":
		if p.ThreadID != thread || thread == "" || p.Cwd != "" && p.Cwd != cwd {
			return false, errTUIControl
		}
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(f.Params, &raw)
		for k := range raw {
			if k != "threadId" && k != "cwd" && k != "excludeTurns" && k != "initialTurnsPage" {
				return false, errTUIControl
			}
		}
		return true, nil
	case "thread/read", "thread/turns/list", "thread/items/list", "thread/goal/get", "thread/schedule/read", "thread/name/set", "thread/unsubscribe", "thread/context/usage", "thread/contextUsage/read":
		return p.ThreadID == thread && thread != "", nil
	case "turn/start", "turn/steer", "turn/interrupt", "thread/compact/start":
		if p.ThreadID != thread || thread == "" || !held() {
			return false, errTUIControl
		}
		return true, nil
	default:
		return false, errTUIControl
	}
}
