//go:build linux && integration

package codexenv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

// TestIntegrationMCPHelper is an actual stdio MCP server in the child test
// executable. It never contacts an external service or requires credentials.
func TestIntegrationMCPHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "mcp-fixture" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &q) != nil || len(q.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		switch q.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "environment-fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "fixture_echo", "description": "Return a local fixture value", "annotations": map[string]bool{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			if len(os.Args) >= 5 && filepath.IsAbs(os.Args[len(os.Args)-2]) {
				_ = os.WriteFile(os.Args[len(os.Args)-2], []byte("called"), 0600)
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "FIXTURE_MCP_OK"}}}
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result}); err != nil {
			return
		}
	}
}

type nativeProbe struct {
	input   io.WriteCloser
	scanner *bufio.Scanner
	next    int
}

func (n *nativeProbe) call(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	n.next++
	id := n.next
	if err := json.NewEncoder(n.input).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	for n.scanner.Scan() {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(n.scanner.Bytes(), &r) != nil || r.ID != id {
			continue
		}
		if len(r.Error) > 0 {
			t.Fatalf("native %s rejected fixture", method)
		}
		return r.Result
	}
	t.Fatalf("native %s ended before response: %v", method, n.scanner.Err())
	return nil
}
func openNativeProbe(t *testing.T, ref *Reference, cwd string) *nativeProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	cmd, cleanup, err := ref.Command(ctx, "codex", []string{"app-server"}, cwd, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	procx.Contain(cmd)
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); procx.KillGroup(cmd); _ = cmd.Wait(); cleanup() })
	n := &nativeProbe{input: input, scanner: bufio.NewScanner(output)}
	n.scanner.Buffer(make([]byte, 4096), 8<<20)
	n.call(t, "initialize", map[string]any{"clientInfo": map[string]string{"name": "tend-fixture", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if err = json.NewEncoder(input).Encode(map[string]any{"method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegrationNativePluginMCPAndHooks(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("native Codex unavailable")
	}
	if err := platformCheck(t.Context()); err != nil {
		t.Skip(err)
	}
	s, p := fixture(t)
	p.UserHome, _ = os.UserHomeDir()
	s.Validate = nil
	source := filepath.Join(t.TempDir(), "fixture")
	if err := os.MkdirAll(filepath.Join(source, ".codex-plugin"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"name": "fixture", "version": "1.0.0", "skills": "./skills", "mcpServers": "./.mcp.json", "hooks": "./hooks/hooks.json"}
	if err := writeJSON(filepath.Join(source, ".codex-plugin/plugin.json"), manifest); err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Join(source, "skills"), "plugin-fixture", "Controlled plugin skill")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mcpMarker := filepath.Join(t.TempDir(), "mcp-called")
	if err = writeJSON(filepath.Join(source, ".mcp.json"), map[string]any{"mcpServers": map[string]any{"fixture": map[string]any{"command": binary, "args": []string{"-test.run=^TestIntegrationMCPHelper$", "--", mcpMarker, "mcp-fixture"}}}}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hookCommand := fmt.Sprintf("touch %s", shellWord(marker))
	if err = writeJSON(filepath.Join(source, "hooks/hooks.json"), map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": hookCommand}}}}}}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "base.toml")
	if err = os.WriteFile(base, []byte("[features]\nhooks = true\n[plugins.\"fixture@source\".mcp_servers.fixture]\ndefault_tools_approval_mode = \"auto\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.BaseConfig = base
	p.Plugins = Selection{Mode: "projection", Allow: []Item{{ID: "fixture@source", Path: source}}}
	status := create(t, s, p)
	pkg := status.Snapshots[0].Packages[0]
	ref, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "integration:plugin")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	n := openNativeProbe(t, ref, cwd)
	hooks := n.call(t, "hooks/list", map[string]any{"cwds": []string{cwd}})
	var catalog struct {
		Data []struct {
			Hooks []struct {
				Key     string `json:"key"`
				Hash    string `json:"currentHash"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err = json.Unmarshal(hooks, &catalog); err != nil {
		t.Fatal(err)
	}
	var key, hash string
	for _, entry := range catalog.Data {
		for _, hook := range entry.Hooks {
			if hook.Command == hookCommand {
				key, hash = hook.Key, hook.Hash
			}
		}
	}
	if key == "" || hash == "" {
		t.Fatal("plugin hook absent from effective native inventory")
	}
	opened := n.call(t, "thread/start", map[string]any{"cwd": cwd, "approvalPolicy": "never", "sandbox": "read-only", "config": map[string]any{"hooks.state": map[string]any{key: map[string]string{"trusted_hash": hash}}}})
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = json.Unmarshal(opened, &thread); err != nil || thread.Thread.ID == "" {
		t.Fatal("fixture thread missing", err)
	}
	mcp := n.call(t, "mcpServerStatus/list", map[string]any{"threadId": thread.Thread.ID})
	if !strings.Contains(string(mcp), "fixture_echo") || !strings.Contains(string(mcp), pkg.LocalID) {
		t.Fatal("plugin MCP tool missing from thread runtime")
	}
	if model := os.Getenv("TEND_CODEX_ENV_MODEL"); model != "" {
		modelRef, pinErr := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "integration:plugin-model")
		if pinErr != nil {
			t.Fatal(pinErr)
		}
		modelRef.Profile.AuthSource = filepath.Join(p.UserHome, ".codex", "auth.json")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		trust := fmt.Sprintf("hooks.state={%q={trusted_hash=%q}}", key, hash)
		cmd, cleanup, err := modelRef.Command(ctx, "codex", []string{"-c", trust, "exec", "--skip-git-repo-check", "--sandbox", "read-only", "--model", model, "--json", "Call the fixture_echo MCP tool once, then reply ENVIRONMENT_OK. Do not run shell commands."}, cwd, os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		procx.Contain(cmd)
		out, err := cmd.CombinedOutput()
		procx.KillGroup(cmd)
		if err != nil {
			t.Fatalf("controlled plugin model turn failed: %v", err)
		}
		if _, err = os.Stat(mcpMarker); err != nil {
			t.Fatalf("controlled model turn did not call fixture MCP; events: %s", out)
		}
		if _, err = os.Stat(marker); err != nil {
			t.Fatal("trusted SessionStart hook did not execute", err)
		}
		t.Log("controlled model turn confirmed plugin MCP call and trusted SessionStart execution")
	}
	t.Log("complete projected plugin: native skill, MCP initialization/tool inventory and hook declaration confirmed")
}
func shellWord(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
