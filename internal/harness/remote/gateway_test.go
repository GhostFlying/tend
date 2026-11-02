package remote

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestTUIConstrainedResumeAndTurn(t *testing.T) {
	raw := []byte(`{"id":4,"method":"thread/resume","params":{"threadId":"thread-a","cwd":"/other","model":"local-model","sandbox":"read-only","config":{"model_provider":"local"},"initialTurnsPage":{"limit":5}}}`)
	data, err := constrainTUI(raw, "thread-a", "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "local") || strings.Contains(string(data), "sandbox") || strings.Contains(string(data), "other") {
		t.Fatalf("client defaults leaked: %s", data)
	}
	forward, err := checkTUI(data, "thread-a", "/workspace", func() bool { return false })
	if err != nil || !forward {
		t.Fatalf("resume rejected: %s %v", data, err)
	}
	data, err = constrainTUI([]byte(`{"id":5,"method":"turn/start","params":{"threadId":"thread-a","cwd":"/other","environments":[{"environmentId":"local"}],"approvalPolicy":"on-request","approvalsReviewer":"user","sandboxPolicy":{"type":"readOnly"},"permissions":{"profile":"local"},"input":[{"type":"text","text":"continue"}]}}`), "thread-a", "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "approvalPolicy") || strings.Contains(string(data), "sandboxPolicy") || strings.Contains(string(data), "permissions") {
		t.Fatalf("frontend permission defaults leaked: %s", data)
	}
	var f struct {
		Params struct {
			Cwd          string              `json:"cwd"`
			Environments []map[string]string `json:"environments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Params.Cwd != "/workspace" || len(f.Params.Environments) != 1 || f.Params.Environments[0]["environmentId"] != "remote" {
		t.Fatalf("wrong environment: %s", data)
	}
	if _, err := checkTUI(data, "thread-a", "/workspace", func() bool { return false }); err == nil {
		t.Fatal("automatic state allowed an owner turn")
	}
	if ok, err := checkTUI(data, "thread-a", "/workspace", func() bool { return true }); err != nil || !ok {
		t.Fatal("held state refused an owner turn", err)
	}
}

func TestFrontendRetriesDisconnectedViewButPreservesNormalExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX terminal adapter")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	for _, exit := range []string{"0", "1"} {
		t.Run(exit, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "starts")
			t.Setenv("TEND_TEST_TUI_STARTS", marker)
			bin := filepath.Join(root, "frontend")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'start\\n' >> \"$TEND_TEST_TUI_STARTS\"\nexit "+exit+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			capability := filepath.Join(root, "capability.json")
			if err := writePrivate(capability, PrivateTUI{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Token: "test", Thread: "original", Cwd: root, Bin: bin}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
			defer cancel()
			_ = RunTUI(ctx, capability, strings.NewReader(""), io.Discard, io.Discard)
			raw, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			starts := strings.Count(string(raw), "start")
			if exit == "0" && starts != 1 || exit == "1" && starts < 2 {
				t.Fatalf("frontend exit %s caused %d starts", exit, starts)
			}
			if frontendExited(capability, "original", root) != (exit == "0") {
				t.Fatal("frontend exit proof did not distinguish normal exit from retry")
			}
		})
	}
}

func TestAttachTUIUsesExitProofAndPublishesNativeIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX terminal adapter")
	}
	for _, receipt := range []string{"none", "original", "stale"} {
		t.Run(receipt, func(t *testing.T) {
			directory := t.TempDir()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			capability := filepath.Join(directory, "central-tui.json")
			original := PrivateTUI{Thread: "thread-a", Cwd: directory, Token: "old-token"}
			if err := writePrivate(capability, original); err != nil {
				t.Fatal(err)
			}
			if receipt != "none" {
				proof := original
				if receipt == "stale" {
					proof.Token = "unrelated-token"
				}
				if err := writePrivate(capability+".exited", proof); err != nil {
					t.Fatal(err)
				}
			}
			created := 0
			oc := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[1] == "list" {
					return []byte(`{"ok":true,"result":{"terminals":[{"handle":"old-shell","connected":true,"tabId":"old-tab","leafId":"leaf"}]}}`), nil
				}
				if args[1] != "create" {
					t.Fatalf("owner shell was changed: %v", args)
				}
				created++
				return []byte(`{"ok":true,"result":{"terminal":{"handle":"new-view","connected":true,"tabId":"new-tab","leafId":"leaf"}}}`), nil
			}}
			var identities []protocol.NativeIdentity
			ex := &execution{listener: listener, token: "rotated-token", stateFile: capability, frontendReady: make(chan struct{}),
				state: workerState{RunID: "run", StateDir: directory, Cwd: directory, ProviderID: "provider", Thread: "thread-a", Terminal: "old-shell"},
				spec: harness.WorkerSpec{RunID: "run", OnThread: func(string) error { return nil }, OnStartupEvent: func(_ context.Context, event harness.WorkerEvent) error {
					if event.Kind != protocol.EventNativeIdentity {
						t.Fatal(event)
					}
					var identity protocol.NativeIdentity
					if err := json.Unmarshal(event.Body, &identity); err != nil {
						t.Fatal(err)
					}
					identities = append(identities, identity)
					return nil
				}}}
			ex.frontendOnce.Do(func() { close(ex.frontendReady) })
			workers := NewWorkers(WorkerOptions{Orca: oc, Helper: "runner-helper"})
			if err := workers.attachTUI(t.Context(), ex); err != nil {
				t.Fatal(err)
			}
			wantTerminal, wantPane, wantCreated := "old-shell", "old-tab:leaf", 0
			if receipt == "original" {
				wantTerminal, wantPane, wantCreated = "new-view", "new-tab:leaf", 1
				select {
				case <-ex.frontendReady:
					t.Fatal("new view reused a previous frontend readiness receipt")
				default:
				}
			}
			if created != wantCreated || len(identities) != 1 || identities[0].State != "known" || identities[0].Terminal != wantTerminal || identities[0].Pane != wantPane || identities[0].SessionID != "thread-a" || identities[0].ProviderID != "provider" {
				t.Fatalf("created=%d identities=%+v", created, identities)
			}
		})
	}
}
func TestTUIRejectsOtherThreadAndBusinessCallbacks(t *testing.T) {
	for _, method := range []string{"thread/read", "thread/resume", "turn/start", "turn/interrupt"} {
		data, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": map[string]string{"threadId": "other"}})
		if _, err := constrainTUI(data, "original", "/workspace"); err == nil {
			t.Fatalf("%s accepted another thread", method)
		}
	}
	for _, data := range []string{`{"id":7,"result":{"success":false}}`, `{"method":"item/tool/call","id":8,"params":{}}`, `{"method":"thread/start","id":9,"params":{}}`} {
		if ok, _ := checkTUI([]byte(data), "original", "/workspace", func() bool { return true }); ok {
			t.Fatalf("unexpected forwarding: %s", data)
		}
	}
}
func TestCenterEnvironmentDoesNotInheritRunnerIdentity(t *testing.T) {
	got := centerEnv([]string{"ORCA_PANE_KEY=parent", "orca_hook=secret", "TRAE_HOME=old", "TRAECLI_HOME=old", "TRAECLI_EXEC_SERVER_URL=old", "CODEX_EXEC_SERVER_URL=old", "TEND_SCRATCH=runner", "PATH=keep", "MODEL_AUTH=keep"}, "center", "ws://executor")
	want := []string{"PATH=keep", "MODEL_AUTH=keep", "TRAE_HOME=center", "TRAECLI_HOME=center", "TRAECLI_EXEC_SERVER_URL=ws://executor", "TEND_ADDON=off"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment: %v", got)
	}
}

func TestTUIEmptyRequestParamsRemainPresent(t *testing.T) {
	for _, method := range []string{"account/read", "collaborationMode/list", "model/list"} {
		data, err := constrainTUI([]byte(`{"id":1,"method":"`+method+`","params":{}}`), "thread", "/workspace")
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]json.RawMessage
		_ = json.Unmarshal(data, &frame)
		if string(frame["params"]) != "{}" {
			t.Fatalf("%s lost request params: %s", method, data)
		}
	}
}
