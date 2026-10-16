//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
)

// TestFault_WorkerIgnoresAddon runs a real worker on a machine where the
// owner installed tend's hooks and MCP server the way `tend setup traex`
// does, and checks that the worker never reaches tend through them (D45):
// the runner marks the worker with TEND_ADDON=off, the hooks see it
// directly and the MCP server through env_vars.
func TestFault_WorkerIgnoresAddon(t *testing.T) {
	dir := t.TempDir()
	tend := filepath.Join(dir, "tend")
	if out, err := exec.Command("go", "build", "-o", tend, "github.com/GhostFlying/tend/cmd/tend").CombinedOutput(); err != nil {
		t.Fatalf("build tend: %v\n%s", err, out)
	}

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Logf("tend server got %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/agent/tools":
			fmt.Fprint(w, `{"tools":[{"name":"tend_context","description":"Read the tend ledger.","input_schema":{"type":"object"}}]}`)
		default:
			fmt.Fprint(w, `{"context":"[tend] the worker must not see this"}`)
		}
	}))
	defer srv.Close()

	cfgDir := filepath.Join(dir, "config")
	writeFile(t, filepath.Join(cfgDir, "config.json"), fmt.Sprintf(`{"server":%q}`, srv.URL))
	writeFile(t, filepath.Join(cfgDir, "credentials.json"), `{"client_id":"cli-0123456789abcdef","owner_token":"o","agent_token":"a"}`)

	// tendw records that the agent started it, then runs tend.
	marks := filepath.Join(dir, "marks")
	if err := os.Mkdir(marks, 0o755); err != nil {
		t.Fatal(err)
	}
	tendw := filepath.Join(dir, "tendw")
	writeFile(t, tendw, fmt.Sprintf("#!/bin/sh\ntouch %q/\"$1-$3\"\nexec %q \"$@\"\n", marks, tend))
	if err := os.Chmod(tendw, 0o755); err != nil {
		t.Fatal(err)
	}

	// The wrapper stands in for the owner's user-level configuration.
	hook := func(event string) string {
		return fmt.Sprintf(`[{hooks=[{type="command",command="%s hook traex %s",timeout=5,additionalContextLimit=0}]}]`, tendw, event)
	}
	agentBin := filepath.Join(dir, "traex")
	overrides := []string{
		"hooks.SessionStart=" + hook("session-start"),
		"hooks.UserPromptSubmit=" + hook("user-prompt-submit"),
		fmt.Sprintf("mcp_servers.tend.command=%q", tendw),
		`mcp_servers.tend.args=["mcp","--agent","traex"]`,
		`mcp_servers.tend.env_vars=["TEND_ADDON","TEND_CONFIG_DIR"]`,
		`mcp_servers.tend.default_tools_approval_mode="approve"`,
	}
	wrapAgent(t, agentBin, overrides)
	// The owner trusted the hooks when installing them, so they also run
	// in the worker.
	work := t.TempDir()
	wrapAgent(t, agentBin, append(overrides, trustHooks(t, agentBin, work)))

	workers := traex.NewWorkers(traex.WorkerOptions{
		Session: traex.Options{Bin: agentBin, Env: []string{"TEND_CONFIG_DIR=" + cfgDir}, Version: "it"},
		Model:   traexModel(),
	})
	spec := json.RawMessage(`{"goal":"Create a file named done.txt containing the word ok. If a tool named tend_context is available, call it first.",` +
		`"acceptance":["done.txt contains ok"],"delivery":"commit"}`)
	runWorker(t, workers, harness.WorkerSpec{RunID: "run_addon", Role: protocol.RoleImplement, Dir: work, ScratchDir: t.TempDir(), Spec: spec, Attempt: 1})

	entries, err := os.ReadDir(marks)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, e := range entries {
		seen = append(seen, e.Name())
	}
	t.Logf("tend started as: %v", seen)
	for _, want := range []string{"hook-session-start", "hook-user-prompt-submit", "mcp-traex"} {
		if _, err := os.Stat(filepath.Join(marks, want)); err != nil {
			t.Errorf("the agent did not start tend %s, so the test proves nothing about it", want)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the worker reached the tend server %d times", n)
	}
}

// wrapAgent writes a traex wrapper that adds config overrides.
func wrapAgent(t *testing.T, path string, overrides []string) {
	t.Helper()
	script := "#!/bin/sh\nexec traex \"$@\""
	for _, o := range overrides {
		script += " -c " + shellQuote(o)
	}
	writeFile(t, path, script+"\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// trustHooks asks an app-server for the hooks it would run in cwd and
// returns the override that marks tend's trusted, as accepting them in the
// TUI does.
func trustHooks(t *testing.T, agentBin, cwd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, agentBin, "app-server")
	cmd.Dir = cwd
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		_ = cmd.Wait()
	}()
	enc := json.NewEncoder(in)
	dec := json.NewDecoder(out)
	call := func(id int, method string, params any) json.RawMessage {
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			var m struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := dec.Decode(&m); err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			if m.ID == nil || *m.ID != id {
				continue
			}
			if m.Error != nil {
				t.Fatalf("%s: %s", method, m.Error)
			}
			return m.Result
		}
	}
	call(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "tend-it", "version": "0"}})
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			Hooks []struct {
				Key         string `json:"key"`
				Command     string `json:"command"`
				CurrentHash string `json:"currentHash"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(call(2, "hooks/list", map[string]any{"cwds": []string{cwd}}), &list); err != nil {
		t.Fatal(err)
	}
	// The keys contain dots, so they go in one inline table rather than
	// in dotted override paths.
	var trust []string
	for _, d := range list.Data {
		for _, h := range d.Hooks {
			if strings.Contains(h.Command, " hook traex ") {
				trust = append(trust, fmt.Sprintf("%q={trusted_hash=%q}", h.Key, h.CurrentHash))
			}
		}
	}
	if len(trust) != 2 {
		t.Fatalf("found %d of tend's hooks to trust, want 2", len(trust))
	}
	return "hooks.state={" + strings.Join(trust, ",") + "}"
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
