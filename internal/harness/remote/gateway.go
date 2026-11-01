package remote

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

func (ws *Workers) gateway(ex *execution) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token, err := newToken()
	if err != nil {
		_ = listener.Close()
		return err
	}
	ex.listener = listener
	ex.token = token
	ex.stateFile = filepath.Join(ex.spec.StateDir, "central-tui.json")
	ex.gateway = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "authentication failed", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/status" {
			if ex.ctx.Err() != nil {
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"active":true}`)
			return
		}
		client, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer client.CloseNow()
		client.SetReadLimit(32 << 20)
		ex.mu.Lock()
		spec := ex.spec
		thread, cwd := ex.state.Thread, ex.state.Cwd
		ex.mu.Unlock()
		upstream, err := spec.NativeDial(ex.ctx, "tui")
		if err != nil {
			return
		}
		defer upstream.Close()
		proxyCtx, cancel := context.WithCancel(ex.ctx)
		defer cancel()
		var requestMu sync.Mutex
		var resumeID string
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer cancel()
			defer client.CloseNow()
			reader := bufio.NewReader(upstream)
			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					return
				}
				var frame nativeFrame
				_ = json.Unmarshal(line, &frame)
				requestMu.Lock()
				isResume := resumeID != "" && string(frame.ID) == resumeID
				requestMu.Unlock()
				if isResume && frame.Result != nil {
					var response struct {
						Thread struct {
							ID string `json:"id"`
						} `json:"thread"`
					}
					if json.Unmarshal(frame.Result, &response) == nil && response.Thread.ID == thread {
						ex.frontendOnce.Do(func() { close(ex.frontendReady) })
					}
				}
				if client.Write(proxyCtx, websocket.MessageText, line[:len(line)-1]) != nil {
					return
				}
			}
		}()
		for {
			_, data, err := client.Read(proxyCtx)
			if err != nil {
				break
			}
			var frame nativeFrame
			if json.Unmarshal(data, &frame) != nil {
				break
			}
			constrained, err := constrainTUI(data, thread, cwd)
			if err != nil {
				_ = writeFrame(proxyCtx, client, map[string]any{"id": frame.ID, "error": map[string]any{"code": -32600, "message": "the terminal is bound to another session"}})
				continue
			}
			if nativeWrite(frame.Method) {
				ex.mu.Lock()
				takeover, held := ex.spec.OnTakeover, ex.ownerHeld
				ex.mu.Unlock()
				if !held {
					if takeover == nil || takeover("native TUI owner input") != nil {
						_ = writeFrame(proxyCtx, client, map[string]any{"id": frame.ID, "error": map[string]any{"code": -32600, "message": "takeover was not confirmed; input was not sent"}})
						continue
					}
					ex.mu.Lock()
					ex.ownerHeld = true
					ex.mu.Unlock()
				}
			}

			if frame.Method == "thread/resume" {
				requestMu.Lock()
				resumeID = string(frame.ID)
				requestMu.Unlock()
			}
			if _, err := upstream.Write(append(constrained, '\n')); err != nil {
				break
			}
		}
		cancel()
		_ = upstream.Close()
		<-done
	})}
	go func() { _ = ex.gateway.Serve(listener) }()
	return nil
}
func nativeWrite(method string) bool {
	switch method {
	case "turn/start", "turn/steer", "turn/interrupt", "thread/compact/start":
		return true
	}
	return false
}

// constrainTUI keeps client-side defaults from changing the run's environment,
// permissions, instructions or model-provider configuration during a resume.
func constrainTUI(data []byte, thread, cwd string) ([]byte, error) {
	var f nativeFrame
	if json.Unmarshal(data, &f) != nil {
		return nil, errTUIControl
	}
	if f.Method == "" {
		return data, nil
	}
	var p map[string]json.RawMessage
	if len(f.Params) > 0 && string(f.Params) != "null" && json.Unmarshal(f.Params, &p) != nil {
		return nil, errTUIControl
	}
	if p == nil {
		p = map[string]json.RawMessage{}
	}
	var id string
	if raw := p["threadId"]; len(raw) > 0 {
		if json.Unmarshal(raw, &id) != nil || id != thread || thread == "" {
			return nil, errTUIControl
		}
	}
	switch f.Method {
	case "thread/resume":
		if id == "" {
			return nil, errTUIControl
		}
		out := map[string]json.RawMessage{"threadId": p["threadId"]}
		for _, k := range []string{"excludeTurns", "initialTurnsPage"} {
			if v := p[k]; len(v) > 0 {
				out[k] = v
			}
		}
		p = out
	case "turn/start":
		if id == "" {
			return nil, errTUIControl
		}
		p["cwd"], _ = json.Marshal(cwd)
		p["environments"], _ = json.Marshal([]map[string]string{{"environmentId": "remote", "cwd": cwd}})
		delete(p, "modelProvider")
		for _, key := range []string{"approvalPolicy", "approvalsReviewer", "sandboxPolicy", "permissions"} {
			delete(p, key)
		}
		delete(p, "runtimeWorkspaceRoots")
		delete(p, "config")
		delete(p, "developerInstructions")
		delete(p, "baseInstructions")
	}
	f.Params, _ = json.Marshal(p)
	out := map[string]any{"method": f.Method}
	if f.ID != nil {
		out["id"] = f.ID
	}
	if f.Method != "initialized" || len(f.Params) > 0 && string(f.Params) != "null" && string(f.Params) != "{}" {
		out["params"] = json.RawMessage(f.Params)
	}
	return json.Marshal(out)
}
func (ws *Workers) attachTUI(ctx context.Context, ex *execution) error {
	if ws.opts.Orca == nil {
		return errors.New("central worker requires its Orca terminal client")
	}
	if runtime.GOOS == "windows" {
		return errors.New("central terminal launcher needs an explicit Windows shell adapter")
	}
	ex.mu.Lock()
	state := ex.state
	ex.mu.Unlock()
	if err := writePrivate(ex.stateFile, PrivateTUI{URL: "ws://" + ex.listener.Addr().String(), Token: ex.token, Thread: state.Thread, Cwd: state.Cwd, Bin: ws.opts.Bin, Model: ws.opts.Model}); err != nil {
		return err
	}
	if state.Terminal != "" {
		terms, err := ws.opts.Orca.Terminals(ctx, "id:"+state.ProviderID)
		if err != nil {
			return err
		}
		for _, t := range terms {
			if t.Handle == state.Terminal && t.Connected {
				return nil
			}
		}
		// A successful listing proves that the old view is no longer connected.
		// Explicit resume can create a fresh view of the same central thread;
		// this does not restart its loop or resend any prior owner input.
		state.Terminal = ""
	}
	if state.LaunchPending {
		return errors.New("native terminal creation outcome is unknown; do not relaunch")
	}
	state.LaunchPending = true
	if err := ex.save(state); err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	command := quote(ws.opts.Helper) + " native-tui --state " + quote(ex.stateFile)
	terminal, err := ws.opts.Orca.CreateTerminal(ctx, state.Cwd, "id:"+state.ProviderID, "Tend · "+ex.spec.RunID, command)
	if err != nil {
		return err
	}
	if terminal.Handle == "" {
		return errors.New("native terminal identity is missing")
	}
	state.Terminal = terminal.Handle
	state.LaunchPending = false
	return ex.save(state)
}
func (ex *execution) save(state workerState) error {
	ex.persistMu.Lock()
	defer ex.persistMu.Unlock()
	ex.mu.Lock()
	spec := ex.spec
	ex.mu.Unlock()
	if spec.OnThread == nil {
		return errors.New("native state writer is unavailable")
	}
	raw, _ := json.Marshal(state)
	if err := spec.OnThread(string(raw)); err != nil {
		return err
	}
	ex.mu.Lock()
	ex.state = state
	ex.mu.Unlock()
	return nil
}

// RunTUI runs only a native frontend. The private file is issued by the live run;
// model authentication and session files remain on the center.
func RunTUI(ctx context.Context, path string, stdin io.Reader, stdout, stderr io.Writer) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg PrivateTUI
	if json.Unmarshal(raw, &cfg) == nil && cfg.Closed {
		return nil
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Token == "" || cfg.URL == "" || cfg.Thread == "" || cfg.Cwd == "" {
		return errors.New("invalid native TUI capability")
	}
	for {
		bin := cfg.Bin
		if bin == "" {
			bin = "traex"
		}
		args := []string{"-c", `model_provider="tend_remote_frontend"`, "-c", `model_providers.tend_remote_frontend.name="Tend remote frontend"`, "-c", `model_providers.tend_remote_frontend.base_url="http://127.0.0.1:1"`, "-c", `model_providers.tend_remote_frontend.wire_api="responses"`, "-c", `model_providers.tend_remote_frontend.requires_openai_auth=false`, "-c", `features.in_app_updates=false`}
		if cfg.Model != "" {
			model, _ := json.Marshal(cfg.Model)
			args = append(args, "-c", "model="+string(model))
		}
		args = append(args, []string{"resume", cfg.Thread, "--include-non-interactive", "--remote", cfg.URL, "--remote-auth-token-env", "TEND_NATIVE_TUI_TOKEN", "--cd", cfg.Cwd, "--no-alt-screen"}...)
		err := runTUIProcess(ctx, bin, args, cfg, path, stdin, stdout, stderr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && frontendAvailable(ctx, cfg) {
			return nil
		}
		// Reconnecting the same view never resends user input. Also accept a
		// rotated capability for the original thread/workspace after cold resume.
		retryAt := time.Now().Add(500 * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				continue
			}
			var next PrivateTUI
			if json.Unmarshal(data, &next) == nil && next.Closed && next.Thread == cfg.Thread && next.Cwd == cfg.Cwd {
				return nil
			}
			if json.Unmarshal(data, &next) != nil || next.Thread != cfg.Thread || next.Cwd != cfg.Cwd || next.Token == "" {
				return errors.New("native TUI binding changed")
			}
			if next.Token != cfg.Token || !time.Now().Before(retryAt) {
				cfg = next
				break
			}
		}
	}
}

func frontendAvailable(ctx context.Context, cfg PrivateTUI) bool {
	address := strings.Replace(cfg.URL, "ws://", "http://", 1) + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
