package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBrokerRejectsBeforeStartingNativeProcesses(t *testing.T) {
	broker := NewBroker(t.Context(), BrokerConfig{Bin: "must-not-start"}, nil)
	defer broker.Close()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, Path+"exec", nil)
	broker.Serve(recorder, request, "run", "exec", func(context.Context) (protocol.RunDesired, error) { return "", errors.New("expired") })
	if recorder.Code != http.StatusForbidden || len(broker.runs) != 0 {
		t.Fatal("unauthorized transport created execution", recorder.Code)
	}
}
func TestGatewayRequiresPrivateCapability(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ex := &execution{ctx: ctx, spec: harness.WorkerSpec{StateDir: t.TempDir()}}
	workers := NewWorkers(WorkerOptions{})
	if err := workers.gateway(ex); err != nil {
		t.Fatal(err)
	}
	defer ex.gateway.Close()
	address := "http://" + ex.listener.Addr().String() + "/status"
	response, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("unauthenticated view accepted", response.StatusCode)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+ex.token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
}

func TestControlRemainsInOriginalSession(t *testing.T) {
	run := &nativeRun{thread: "original", cwd: "/workspace"}
	for _, method := range []string{"thread/read", "thread/resume", "turn/start", "turn/interrupt"} {
		raw, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": map[string]string{"threadId": "other"}})
		if _, err := constrainControl(raw, run, protocol.DesiredRun); err == nil {
			t.Fatalf("%s escaped root", method)
		}
	}
	for _, method := range []string{"thread/start", "thread/fork", "command/exec", "config/read", "account/login/start"} {
		raw, _ := json.Marshal(map[string]any{"id": 2, "method": method, "params": map[string]string{"threadId": "original"}})
		if _, err := constrainControl(raw, run, protocol.DesiredRun); err == nil {
			t.Fatalf("unexpected control method: %s", method)
		}
	}
	start := &nativeRun{cwd: "/workspace"}
	raw := []byte(`{"id":3,"method":"thread/start","params":{"cwd":"/center","environments":[{"environmentId":"local"}],"config":{"mcp_servers":{}},"sandbox":"read-only"}}`)
	normalized, err := constrainControl(raw, start, protocol.DesiredRun)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(normalized), "/center") || strings.Contains(string(normalized), "local") || strings.Contains(string(normalized), "mcp_servers") || !strings.Contains(string(normalized), "remote") {
		t.Fatal(string(normalized))
	}
	if _, err := constrainControl(raw, start, protocol.DesiredRun); err == nil {
		t.Fatal("another start accepted before first receipt")
	}
	if _, err := constrainControl([]byte(`{"id":4,"method":"turn/start","params":{"threadId":"original"}}`), run, protocol.DesiredHeld); err == nil {
		t.Fatal("automatic control wrote during held")
	}
}

func TestReleaseEvictsFinalizedNativeExecution(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			dir := t.TempDir()
			ex := &execution{state: workerState{Kind: "central-traex", RunID: "run", Cwd: "/workspace", StateDir: dir, Thread: "thread", ExecutorStopped: true}, closed: make(chan struct{}), stateFile: filepath.Join(dir, "central-tui.json")}
			ex.closeOnce.Do(func() { close(ex.closed) })
			workers := NewWorkers(WorkerOptions{})
			workers.runs["run"] = ex
			var err error
			if held {
				err = workers.ReleaseHistorical(t.Context(), "run", "thread", true, 1)
			} else {
				err = workers.Release(t.Context(), "run", "thread")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(workers.runs) != 0 {
				t.Fatal("finalized execution retained")
			}
			raw, err := os.ReadFile(ex.stateFile)
			if err != nil {
				t.Fatal(err)
			}
			var cfg PrivateTUI
			if json.Unmarshal(raw, &cfg) != nil || !cfg.Closed {
				t.Fatal("frontend capability not revoked")
			}
		})
	}
}

func TestColdHeldReleaseRequiresOriginalStopProof(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			dir := t.TempDir()
			pin, _ := json.Marshal(workerState{Kind: "central-traex", RunID: "run", Thread: "original", Cwd: "/workspace", StateDir: dir, ExecutorStopped: stopped})
			workers := NewWorkers(WorkerOptions{})
			err := workers.ReleaseHistorical(t.Context(), "run", string(pin), true, 1)
			if !stopped {
				if !errors.Is(err, harness.ErrHeld) {
					t.Fatalf("unconfirmed execution was released: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "central-tui.json"))
			var capability PrivateTUI
			if err != nil || json.Unmarshal(raw, &capability) != nil || !capability.Closed || capability.Thread != "original" {
				t.Fatalf("stopped run retained frontend authority: %s %v", raw, err)
			}
		})
	}
}

func TestRemoteBrokerHelper(t *testing.T) {
	if os.Getenv("TEND_NATIVE_TEST_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("TEND_NATIVE_TEST_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0o600)
	}
	if os.Getenv("TEND_NATIVE_TEST_MODE") == "hang" {
		for {
			time.Sleep(time.Minute)
		}
	}
	var address string
	for i, arg := range os.Args {
		if arg == "--listen" && i+1 < len(os.Args) {
			address = strings.TrimPrefix(os.Args[i+1], "ws://")
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	})}
	_ = server.Serve(listener)
}
func TestBrokerRevokesDuringStartupAndBeforeExecutorAttach(t *testing.T) {
	for _, mode := range []string{"hang", "serve"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TEND_NATIVE_TEST_HELPER", "1")
			t.Setenv("TEND_NATIVE_TEST_MODE", mode)
			marker := filepath.Join(t.TempDir(), "started")
			t.Setenv("TEND_NATIVE_TEST_MARKER", marker)
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			broker := NewBroker(ctx, BrokerConfig{Bin: bin, Cwd: t.TempDir(), Args: []string{"-test.run=^TestRemoteBrokerHelper$", "--"}}, nil)
			defer broker.Close()
			var allowed atomic.Bool
			allowed.Store(true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				broker.Serve(w, r, "run", "exec", func(context.Context) (protocol.RunDesired, error) {
					if !allowed.Load() {
						return "", errors.New("revoked")
					}
					return protocol.DesiredRun, nil
				})
			}))
			defer server.Close()
			client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseNow()
			if err := writeFrame(ctx, client, Hello{Cwd: "/workspace"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.Read(ctx); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native process did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if mode == "hang" {
				allowed.Store(false)
			} else {
				_ = client.CloseNow()
			}
			deadline = time.Now().Add(3 * time.Second)
			for {
				broker.mu.Lock()
				remaining := len(broker.runs)
				broker.mu.Unlock()
				if remaining == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("revoked startup retained native process and run entry")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
