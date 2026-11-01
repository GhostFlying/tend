//go:build integration

package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/coder/websocket"
)

func TestNativeRemoteLifecycle(t *testing.T) {
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement})
}

func TestNativeRemoteHeldVerdict(t *testing.T) {
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleAccept})
}

func TestNativeRemoteClosedViewHandback(t *testing.T) {
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		t.Skip("fixture verifies confirmed view closure through the Orca RPC boundary")
	}
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement, closeView: true})
}

func TestNativeRemotePauseBeforeFirstBrief(t *testing.T) {
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		t.Skip("fixture pauses while the native view is deliberately not attached")
	}
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement, pauseBeforeBrief: true})
}

func TestNativeRemoteConfirmedPause(t *testing.T) {
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement, pauseTurn: true})
}

func TestNativeRemoteUnconfirmedPause(t *testing.T) {
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement, pauseTurn: true, rejectInterrupt: true})
}

func TestNativeRemotePauseRejectsFirstBrief(t *testing.T) {
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		t.Skip("fixture injects one server-side pause before the runner observes it")
	}
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement, pauseAtBrief: true})
}

func TestNativeRemotePauseDuringHandback(t *testing.T) {
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		t.Skip("fixture pauses an explicitly handed-back native turn before it is idle")
	}
	nativeRemoteLifecycle(t, nativeScenario{role: protocol.RoleImplement, pauseDuringHandback: true})
}

type nativeScenario struct {
	role                protocol.RunRole
	closeView           bool
	pauseBeforeBrief    bool
	pauseTurn           bool
	rejectInterrupt     bool
	pauseAtBrief        bool
	pauseDuringHandback bool
}

func nativeRemoteLifecycle(t *testing.T, scenario nativeScenario) {
	t.Helper()
	role, closeView, pauseBeforeBrief := scenario.role, scenario.closeView, scenario.pauseBeforeBrief
	bin := os.Getenv("TEND_TRAEX_BIN")
	if bin == "" {
		bin = "traex"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("TraeX installation required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	home := filepath.Join(root, "center-home")
	stateDir := filepath.Join(root, "runner-state")
	for _, p := range []string{workspace, home, stateDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var phaseMu sync.Mutex
	phase := "automatic"
	steps := map[string]int{}
	var outputs []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		raw, _ := json.Marshal(body["tools"])
		phaseMu.Lock()
		p := phase
		n := steps[p]
		if strings.Contains(string(raw), "Bash") {
			steps[p]++
		}
		input, _ := json.Marshal(body["input"])
		outputs = append(outputs, string(input))
		phaseMu.Unlock()
		if strings.Contains(string(raw), "Bash") && !strings.Contains(string(input), "original_remote_task_goal") {
			t.Error("model turn lost the frozen task brief")
		}
		var item map[string]any
		if !strings.Contains(string(raw), "Bash") {
			item = map[string]any{"type": "message", "role": "assistant", "id": "title", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Remote verification", "annotations": []any{}}}}
		} else if n == 0 {
			command := "printf '%s' " + p + " > " + filepath.Join(workspace, p+".txt") + "; pwd"
			if p == "fenced" {
				command += "; sleep 3; printf finished > " + filepath.Join(workspace, "fence-finished")
			}
			if p == "automatic" {
				sleep := "90"
				if scenario.rejectInterrupt {
					sleep = "1"
				}
				command += "; sleep " + sleep + "; printf finished > " + filepath.Join(workspace, "unexpected-finished")
			}
			if p == "owner" && scenario.pauseDuringHandback {
				command += "; sleep 3; printf finished > " + filepath.Join(workspace, "owner-after-pause")
			}
			args, _ := json.Marshal(map[string]any{"command": command, "timeout": 30000})
			item = map[string]any{"type": "function_call", "id": "fc_" + p, "call_id": "cmd_" + p, "name": "Bash", "arguments": string(args)}
		} else if n == 1 {
			args, _ := json.Marshal(map[string]any{"report": map[string]string{"body": "report_" + p}})
			item = map[string]any{"type": "function_call", "id": "dyn_" + p, "call_id": "result_" + p, "name": "submit_result", "arguments": string(args)}
			if role == protocol.RoleAccept {
				args, _ = json.Marshal(map[string]any{"verdict": "passed", "evidence": map[string]string{"phase": p}})
				item["name"], item["arguments"] = "submit_verdict", string(args)
				if p == "after" {
					item = map[string]any{"type": "message", "role": "assistant", "id": "msg_after", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "The verdict was already recorded during the owner turn.", "annotations": []any{}}}}
				}
			}
		} else {
			item = map[string]any{"type": "message", "role": "assistant", "id": "msg_" + p, "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "DONE_" + p, "annotations": []any{}}}}
		}
		response := map[string]any{"id": "resp_" + p + fmt.Sprint(n), "status": "completed", "model": "probe-model", "output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []any{map[string]any{"type": "response.created", "response": map[string]string{"id": "resp_" + p}}, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, map[string]any{"type": "response.completed", "response": response}} {
			raw, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer model.Close()
	args := []string{"-c", `model_provider="probe"`, "-c", `model="probe-model"`, "-c", `model_providers.probe.name="probe"`, "-c", `model_providers.probe.base_url="` + model.URL + `/v1"`, "-c", `model_providers.probe.wire_api="responses"`, "-c", `model_providers.probe.requires_openai_auth=false`}
	for _, feature := range []string{"hooks", "plugins", "apps", "memories", "browser_use", "browser_use_external", "computer_use", "image_generation", "code_mode_host", "in_app_updates"} {
		args = append(args, "-c", "features."+feature+"=false")
	}
	broker := NewBroker(ctx, BrokerConfig{Bin: bin, Home: home, Cwd: root, Args: args}, nil)
	defer broker.Close()
	var held atomic.Bool
	var paused atomic.Bool
	var authorized atomic.Bool
	authorized.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-secret" || r.Header.Get("X-Tend-Run") != "run-a" {
			http.Error(w, "forbidden", 403)
			return
		}
		broker.Serve(w, r, "run-a", strings.TrimPrefix(r.URL.Path, Path), func(context.Context) (protocol.RunDesired, error) {
			if !authorized.Load() {
				return "", errors.New("expired")
			}
			if held.Load() {
				return protocol.DesiredHeld, nil
			}
			if paused.Load() {
				return protocol.DesiredPaused, nil
			}
			return protocol.DesiredRun, nil
		})
	}))
	defer server.Close()
	var closedView atomic.Bool
	oc := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "list" {
			if closedView.Load() {
				return []byte(`{"ok":true,"result":{"terminals":[]}}`), nil
			}
			return []byte(`{"ok":true,"result":{"terminals":[{"handle":"terminal-a","connected":true}]}}`), nil
		}
		return []byte(`{"ok":true,"result":{"terminal":{"handle":"terminal-a","connected":true}}}`), nil
	}}
	providerID := "test-provider"
	if realOrca := os.Getenv("TEND_NATIVE_ORCA_BIN"); realOrca != "" {
		oc = &orca.Client{Bin: realOrca}
		group, err := oc.CreateGroup(ctx, "Tend native remote test", root)
		if err != nil {
			t.Fatal(err)
		}
		folder, err := oc.CreateFolder(ctx, group.ID, workspace, "Remote workspace")
		if err != nil {
			t.Fatal(err)
		}
		providerID = "folder:" + folder.ID
	}
	helper := os.Getenv("TEND_NATIVE_RUNNER_BIN")
	if helper == "" {
		helper = "unused-test-helper"
	}
	waitFrontend := time.Duration(-1)
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		waitFrontend = 20 * time.Second
	}
	if pauseBeforeBrief {
		waitFrontend = 20 * time.Second
	}
	workers := NewWorkers(WorkerOptions{FrontendWait: waitFrontend, Bin: bin, Helper: helper, Orca: oc, Model: "probe-model"})
	defer workers.Close()
	var pinMu sync.Mutex
	pin := ""
	save := func(s string) error { pinMu.Lock(); pin = s; pinMu.Unlock(); return nil }
	var takeoverMu sync.Mutex
	retainedUsage := make(chan harness.WorkerEvent, 32)
	attemptCtx, stopAttempt := context.WithCancelCause(ctx)
	defer stopAttempt(nil)
	spec := harness.WorkerSpec{Lifetime: ctx, RunID: "run-a", Dir: workspace, ProviderID: providerID, StateDir: stateDir, ScratchDir: workspace, Role: role, Spec: json.RawMessage(`{"goal":"original_remote_task_goal","title":"remote lifecycle","deliverables":["report"]}`), OnThread: save, NativeDial: func(c context.Context, ch string) (io.ReadWriteCloser, error) {
		transport, err := Dial(c, server.Client(), server.URL, "run-a", "run-secret", ch)
		if err == nil && ch == "control" && scenario.rejectInterrupt {
			return interruptRefusingTransport{transport}, nil
		}
		if err == nil && ch == "control" && scenario.pauseAtBrief {
			return pauseBriefTransport{ReadWriteCloser: transport, paused: &paused, once: &sync.Once{}}, nil
		}
		return transport, err
	}, OnRetainedEvent: func(_ context.Context, ev harness.WorkerEvent) error {
		retainedUsage <- ev
		return nil
	}, OnTakeover: func(string) error {
		held.Store(true)
		takeoverMu.Lock()
		stopAttempt(harness.ErrHeld)
		takeoverMu.Unlock()
		return nil
	}}
	if scenario.pauseAtBrief {
		if _, err := workers.Start(attemptCtx, spec); !errors.Is(err, harness.ErrRejected) || !errors.Is(err, harness.ErrPaused) {
			t.Fatal("server-side startup pause was not retained as a definite refusal", err)
		}
		pinMu.Lock()
		spec.Thread = pin
		pinMu.Unlock()
		var state workerState
		if json.Unmarshal([]byte(spec.Thread), &state) != nil || state.BriefPending || state.ExecutorStopped || state.Reconcile {
			t.Fatal("definite refusal became an unknown send or a stopped session")
		}
		paused.Store(false)
	}
	if pauseBeforeBrief {
		startup := make(chan error, 1)
		go func() { _, err := workers.Start(attemptCtx, spec); startup <- err }()
		waitUntil(t, ctx, func() bool {
			pinMu.Lock()
			defer pinMu.Unlock()
			var state workerState
			return json.Unmarshal([]byte(pin), &state) == nil && state.Terminal != ""
		})
		paused.Store(true)
		stopAttempt(harness.ErrPaused)
		if err := <-startup; err == nil {
			t.Fatal("startup was not paused")
		}
		phaseMu.Lock()
		started := steps["automatic"]
		phaseMu.Unlock()
		if started != 0 {
			t.Fatal("model was started before native frontend readiness")
		}
		pinMu.Lock()
		spec.Thread = pin
		pinMu.Unlock()
		paused.Store(false)
		workers.opts.FrontendWait = -1
		spec.Attempt = 2
		attemptCtx, stopAttempt = context.WithCancelCause(ctx)
		defer stopAttempt(nil)
	}
	worker, err := workers.Start(attemptCtx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := spec.NativeDial(ctx, "control"); err == nil {
		_ = second.Close()
		t.Fatal("a second native control writer was accepted")
	}
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range worker.Events() {
		}
	}()
	waitUntil(t, ctx, func() bool { _, e := os.Stat(filepath.Join(workspace, "automatic.txt")); return e == nil })
	workers.mu.Lock()
	ex := workers.runs["run-a"]
	workers.mu.Unlock()
	if scenario.pauseTurn {
		paused.Store(true)
		stopAttempt(harness.ErrPaused)
		if _, err := worker.Wait(); !errors.Is(err, harness.ErrPaused) {
			t.Fatal("pause result", err)
		}
		<-eventsDone
		pinMu.Lock()
		spec.Thread = pin
		pinMu.Unlock()
		if scenario.rejectInterrupt {
			select {
			case <-ex.closed:
			default:
				t.Fatal("unconfirmed pause retained a live executor")
			}
			var state workerState
			if json.Unmarshal([]byte(spec.Thread), &state) != nil || !state.Reconcile || !state.ExecutorStopped {
				t.Fatal("unconfirmed pause lost reconciliation or stop proof")
			}
			time.Sleep(1500 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(workspace, "unexpected-finished")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("command survived reported pause")
			}
			cold := NewWorkers(workers.opts)
			defer cold.Close()
			if _, err := cold.Start(ctx, spec); err == nil || !strings.Contains(err.Error(), "reconciliation") {
				t.Fatal("unknown native stop was resumed", err)
			}
			t.Log("refused interrupt fenced execution and blocked unreconciled resume")
			return
		}
		select {
		case <-ex.closed:
			t.Fatal("confirmed idle pause lost its native session")
		default:
		}
		phaseMu.Lock()
		phase = "after"
		phaseMu.Unlock()
		paused.Store(false)
		resumed, err := workers.Start(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		for range resumed.Events() {
		}
		result, err := resumed.Wait()
		if err != nil || result.Result == nil || result.Result.Report.Body != "report_after" {
			t.Fatal("confirmed pause did not resume original task", result, err)
		}
		t.Log("confirmed idle pause retained and resumed the original native session")
		return
	}
	raw, err := os.ReadFile(ex.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var cfg PrivateTUI
	if json.Unmarshal(raw, &cfg) != nil {
		t.Fatal("invalid TUI capability")
	}
	ex.mu.Lock()
	terminalID := ex.state.Terminal
	ex.mu.Unlock()
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		for {
			broker.mu.Lock()
			run := broker.runs["run-a"]
			broker.mu.Unlock()
			ready := false
			if run != nil {
				run.mu.Lock()
				ready = run.tuiResumed
				run.mu.Unlock()
			}
			if ready {
				break
			}
			screen, readErr := oc.ReadTerminalScreen(ctx, terminalID)
			if readErr != nil {
				t.Log("screen read", readErr)
			}
			if evidence := os.Getenv("TEND_NATIVE_EVIDENCE_DIR"); evidence != "" {
				_ = os.WriteFile(filepath.Join(evidence, "frontend-screen.txt"), []byte(screen), 0o600)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("native TUI did not attach; last screen: %s", screen)
			case <-time.After(300 * time.Millisecond):
			}
		}
		t.Log("actual Orca-launched native TUI resumed the central thread")
	}

	native, _, err := websocket.Dial(ctx, cfg.URL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer native.CloseNow()
	native.SetReadLimit(32 << 20)
	rpc := func(id int, method string, params any) map[string]any {
		t.Helper()
		if err := writeFrame(ctx, native, map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			_, data, err := native.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if m["method"] == "item/tool/call" {
				t.Fatal("TUI received a Tend tool request")
			}
			if m["id"] == float64(id) {
				if m["error"] != nil {
					t.Fatalf("%s: %s", method, data)
				}
				return m
			}
		}
	}
	rpc(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "tui-probe", "version": "0"}, "capabilities": map[string]bool{"experimentalApi": true}})
	_ = writeFrame(ctx, native, map[string]string{"method": "initialized"})
	rpc(2, "thread/resume", map[string]any{"threadId": cfg.Thread, "sandbox": "read-only", "cwd": "/incorrect", "config": map[string]string{"model_provider": "wrong"}})
	threadID, turnID := worker.(harness.LiveMessenger).MessageTarget()
	if threadID != cfg.Thread {
		t.Fatal("TUI is not attached to original thread")
	}
	phaseMu.Lock()
	phase = "owner"
	phaseMu.Unlock()
	if os.Getenv("TEND_NATIVE_ORCA_BIN") == "" {
		rpc(3, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID})
	} else {
		waitUntil(t, ctx, func() bool {
			screen, _ := oc.ReadTerminalScreen(ctx, terminalID)
			if evidence := os.Getenv("TEND_NATIVE_EVIDENCE_DIR"); evidence != "" {
				_ = os.WriteFile(filepath.Join(evidence, "frontend-screen.txt"), []byte(screen), 0o600)
			}
			return strings.Contains(screen, "sleep 90")
		})
		if _, err := oc.SendInput(ctx, orca.SendOptions{Terminal: terminalID, Text: "\x1b"}); err != nil {
			t.Fatal(err)
		}
		for {
			_, data, err := native.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var frame nativeFrame
			_ = json.Unmarshal(data, &frame)
			if frame.Method == "turn/completed" {
				break
			}
		}
	}
	_, err = worker.Wait()
	<-eventsDone
	if !errors.Is(err, harness.ErrHeld) {
		t.Fatalf("automatic worker: %v", err)
	}
	select {
	case <-ex.closed:
		t.Fatal("held execution closed")
	default:
	}
	if os.Getenv("TEND_NATIVE_ORCA_BIN") == "" {
		rpc(4, "turn/start", map[string]any{
			"threadId": threadID, "input": []map[string]string{{"type": "text", "text": "OWNER_CHECK"}},
			"approvalPolicy": "on-request", "sandboxPolicy": map[string]string{"type": "readOnly"},
			"permissions": map[string]any{"fileSystem": map[string]any{"write": []string{}}},
		})
	} else {
		for _, text := range []string{"OWNER_CHECK", "\r"} {
			if _, err := oc.SendInput(ctx, orca.SendOptions{Terminal: terminalID, Text: text}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if scenario.pauseDuringHandback {
		waitUntil(t, ctx, func() bool { _, err := os.Stat(filepath.Join(workspace, "owner.txt")); return err == nil })
		held.Store(false)
		pinMu.Lock()
		spec.Thread = pin
		pinMu.Unlock()
		spec.HandBack = true
		retryCtx, stopRetry := context.WithCancelCause(ctx)
		defer stopRetry(nil)
		started := make(chan error, 1)
		go func() { _, err := workers.Start(retryCtx, spec); started <- err }()
		waitUntil(t, ctx, func() bool { ex.mu.Lock(); defer ex.mu.Unlock(); return !ex.ownerHeld })
		paused.Store(true)
		stopRetry(harness.ErrPaused)
		select {
		case err := <-started:
			if !errors.Is(err, harness.ErrPaused) {
				t.Fatal("handback pause lost control cause", err)
			}
		case <-ctx.Done():
			t.Fatal("paused handback did not stop")
		}
		select {
		case <-ex.closed:
		default:
			t.Fatal("handback pause retained the active native turn")
		}
		time.Sleep(3100 * time.Millisecond)
		if _, err := os.Stat(filepath.Join(workspace, "owner-after-pause")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("handed-back command continued after reported pause")
		}
		t.Log("pause during handback fenced the still-active native turn")
		return
	}
	for {
		_, data, err := native.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m nativeFrame
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Method == "item/tool/call" {
			t.Fatal("TUI raced a business callback")
		}
		if m.Method == "turn/completed" {
			break
		}
	}
	if os.Getenv("TEND_NATIVE_ORCA_BIN") != "" {
		waitUntil(t, ctx, func() bool {
			screen, _ := oc.ReadTerminalScreen(ctx, terminalID)
			if evidence := os.Getenv("TEND_NATIVE_EVIDENCE_DIR"); evidence != "" {
				_ = os.WriteFile(filepath.Join(evidence, "owner-screen.txt"), []byte(screen), 0o600)
			}
			return strings.Contains(screen, "DONE_owner")
		})
		t.Log("native TUI keyboard interruption, owner input and rendered tool result verified")
	}
	phaseMu.Lock()
	phase = "after"
	phaseMu.Unlock()
	closedView.Store(closeView)
	ownerUsage := false
	for !ownerUsage {
		select {
		case ev := <-retainedUsage:
			var sample protocol.TokenUsageSample
			if ev.Kind == "usage.request" && json.Unmarshal(ev.Body, &sample) == nil && sample.TurnID != turnID && sample.Total != nil {
				ownerUsage = true
			}
		case <-ctx.Done():
			t.Fatal("owner-held model usage was not persisted")
		}
	}
	held.Store(false)
	takeoverMu.Lock()
	attemptCtx, stopAttempt = context.WithCancelCause(ctx)
	takeoverMu.Unlock()
	defer stopAttempt(nil)
	pinMu.Lock()
	spec.Thread = pin
	pinMu.Unlock()
	spec.HandBack = true
	resumed, err := workers.Start(attemptCtx, spec)
	if err != nil {
		t.Fatal(err)
	}
	for range resumed.Events() {
	}
	result, err := resumed.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if role == protocol.RoleAccept {
		if result.Verdict == nil || result.Verdict.Verdict != protocol.VerdictPassed || !strings.Contains(string(result.Verdict.Evidence), "owner") {
			t.Fatalf("lost acknowledged owner-held verdict: %+v", result)
		}
	} else if result.Result == nil || result.Result.Report == nil || result.Result.Report.Body != "report_after" {
		t.Fatalf("missing result: %+v", result)
	}
	phaseMu.Lock()
	handbackDelivered := strings.Contains(strings.Join(outputs, "\n"), "The owner has handed the task back")
	phaseMu.Unlock()
	if !handbackDelivered {
		t.Fatal("handback was described as an ordinary interrupted attempt")
	}
	for _, p := range []string{"automatic", "owner", "after"} {
		if _, err := os.Stat(filepath.Join(workspace, p+".txt")); err != nil {
			t.Fatalf("remote command %s missing: %v", p, err)
		}
	}
	if role == protocol.RoleAccept {
		t.Log("acknowledged owner-held verdict survived explicit handback")
		return
	}
	if _, err := os.Stat(filepath.Join(workspace, "unexpected-finished")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted command finished")
	}
	// A completed, durably stopped executor may cold-resume its center history.
	phaseMu.Lock()
	phase = "cold"
	phaseMu.Unlock()
	pinMu.Lock()
	spec.Thread = pin
	pinMu.Unlock()
	var stopped workerState
	_ = json.Unmarshal([]byte(spec.Thread), &stopped)
	if !stopped.ExecutorStopped {
		t.Fatal("executor stop proof was not persisted")
	}
	waitUntil(t, ctx, func() bool { broker.mu.Lock(); defer broker.mu.Unlock(); return broker.runs["run-a"] == nil })
	cold := NewWorkers(workers.opts)
	defer cold.Close()
	cw, err := cold.Start(ctx, spec)
	if err != nil {
		t.Fatal("cold resume", err)
	}
	for range cw.Events() {
	}
	coldResult, err := cw.Wait()
	if err != nil || coldResult.Result == nil || coldResult.Result.Report.Body != "report_cold" {
		t.Fatal("cold result", coldResult, err)
	}
	// Revoking authority must close execution, including an in-flight command.
	phaseMu.Lock()
	phase = "fenced"
	phaseMu.Unlock()
	pinMu.Lock()
	spec.Thread = pin
	pinMu.Unlock()
	waitUntil(t, ctx, func() bool { broker.mu.Lock(); defer broker.mu.Unlock(); return broker.runs["run-a"] == nil })
	fenced := NewWorkers(workers.opts)
	defer fenced.Close()
	fw, err := fenced.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	fdone := make(chan struct{})
	go func() {
		defer close(fdone)
		for range fw.Events() {
		}
	}()
	waitUntil(t, ctx, func() bool { _, e := os.Stat(filepath.Join(workspace, "fenced.txt")); return e == nil })
	authorized.Store(false)
	if _, err := fw.Wait(); err == nil {
		t.Fatal("revoked execution completed")
	}
	<-fdone
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(3500 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(workspace, "fence-finished")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("command survived execution revocation")
	}

	rollouts, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*.jsonl"))
	if len(rollouts) != 1 {
		t.Fatalf("center rollout count: %d", len(rollouts))
	}
	phaseMu.Lock()
	all := strings.Join(outputs, "\n")
	phaseMu.Unlock()
	if !strings.Contains(all, "result recorded; finish your turn") {
		t.Fatal("Tend callback was not consumed by model")
	}
	t.Log("central session, remote shell, held TUI, callback ownership, handback, cold resume and fencing passed")
}

type interruptRefusingTransport struct{ io.ReadWriteCloser }

type pauseBriefTransport struct {
	io.ReadWriteCloser
	paused *atomic.Bool
	once   *sync.Once
}

func (t pauseBriefTransport) Write(data []byte) (int, error) {
	var frame nativeFrame
	if json.Unmarshal(data, &frame) == nil && frame.Method == "thread/inject_items" {
		t.once.Do(func() { t.paused.Store(true) })
	}
	return t.ReadWriteCloser.Write(data)
}

func (t interruptRefusingTransport) Write(data []byte) (int, error) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(data, &frame) == nil && string(frame["method"]) == `"turn/interrupt"` {
		frame["method"] = json.RawMessage(`"native/test_interrupt_refused"`)
		altered, _ := json.Marshal(frame)
		_, err := t.ReadWriteCloser.Write(append(altered, '\n'))
		if err != nil {
			return 0, err
		}
		return len(data), nil
	}
	return t.ReadWriteCloser.Write(data)
}
func waitUntil(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
