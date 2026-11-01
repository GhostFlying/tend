package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
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
