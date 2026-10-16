package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runScenarios drives `<bin> app-server` the way tend-runner drives a
// worker, with the probe installed as hooks and as a stdio MCP server
// through -c overrides (standing in for a user-level install). It covers
// what `exec` cannot: real compaction through thread/compact/start, and
// whether TEND_ADDON set on the app-server reaches the hook and MCP
// children.
func runScenarios(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	bin := fs.String("bin", "traex", "agent binary: codex or traex")
	out := fs.String("out", "", "output directory (default spikes/agenthooks/out/<bin>-<time>)")
	forward := fs.Bool("forward-env", false, "add env_vars = [\"TEND_ADDON\"] to the MCP server config")
	fs.Parse(args)

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(filepath.Dir(self), fmt.Sprintf("%s-%s", filepath.Base(*bin), time.Now().Format("150405")))
	}
	// Hooks and the MCP server run in the thread's cwd, so every path they
	// get must be absolute.
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// The thread's cwd must not be inside a repository with an AGENTS.md.
	cwd, err := os.MkdirTemp("", "agenthooks-cwd-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logPath := filepath.Join(dir, "probe.jsonl")
	tr, err := newTranscript(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	label := filepath.Base(*bin) + "-appserver"
	hook := fmt.Sprintf(`[{hooks=[{type="command",command="%s hook %s %s codex",timeout=5}]}]`, self, logPath, label)
	cargs := []string{"app-server",
		"-c", "hooks.SessionStart=" + hook,
		"-c", "hooks.UserPromptSubmit=" + hook,
		"-c", fmt.Sprintf(`mcp_servers.probe.command=%q`, self),
		"-c", fmt.Sprintf(`mcp_servers.probe.args=["mcp",%q,%q]`, logPath, label),
		"-c", `mcp_servers.probe.default_tools_approval_mode="approve"`,
	}
	if *forward {
		cargs = append(cargs, "-c", `mcp_servers.probe.env_vars=["TEND_ADDON"]`)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	os.Setenv("TEND_ADDON", "off") // what tend-runner would set for a worker
	c, err := startClient(ctx, *bin, cargs, tr, func(method string, params json.RawMessage) (any, error) {
		tr.note("unexpected server request %s", method)
		return nil, fmt.Errorf("not supported: %s", method)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer c.close()

	results := map[string]any{"bin": *bin, "forward_env": *forward, "cwd": cwd}
	defer func() {
		b, _ := json.MarshalIndent(results, "", "  ")
		os.WriteFile(filepath.Join(dir, "results.json"), b, 0o644)
		fmt.Println(string(b))
	}()

	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "agenthooks", "version": "0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		results["error"] = "initialize: " + err.Error()
		return 1
	}
	c.notify("initialized", nil)

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.call(ctx, "thread/start", map[string]any{
		"cwd": cwd, "approvalPolicy": "never", "sandbox": "danger-full-access",
		"config": map[string]any{"bypass_hook_trust": true},
	}, &started); err != nil {
		results["error"] = "thread/start: " + err.Error()
		return 1
	}
	thread := started.Thread.ID
	results["thread"] = thread

	ask := func(key, prompt string) {
		var resp struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := c.call(ctx, "turn/start", map[string]any{
			"threadId": thread,
			"input":    []any{map[string]any{"type": "text", "text": prompt, "text_elements": []any{}}},
		}, &resp); err != nil {
			results[key] = "error: " + err.Error()
			return
		}
		results[key] = waitTurn(ctx, c, resp.Turn.ID)
	}

	ask("1_before_compact", "Hooks in this session add tend probe codewords (strings starting with PROBE-) to your context. "+
		"Reply with every such codeword you were given, one per line, then call the probe_echo tool with text hi and quote its output.")
	if err := c.call(ctx, "thread/compact/start", map[string]any{"threadId": thread}, nil); err != nil {
		results["2_compact"] = "error: " + err.Error()
	} else {
		results["2_compact"] = collectUntil(ctx, c, "turn/completed", 5*time.Minute)
	}
	ask("3_after_compact", "Reply with every tend probe codeword (PROBE-...) in your context, one per line, "+
		"and say which ones arrived after the most recent compaction.")

	results["probe_log"] = summarizeLog(logPath)
	return 0
}

type turnOutcome struct {
	Status string   `json:"status"`
	Text   string   `json:"text"`
	Items  []string `json:"items"`
}

func waitTurn(ctx context.Context, c *client, turnID string) turnOutcome {
	var res turnOutcome
	for {
		select {
		case <-ctx.Done():
			res.Status = "ctx-done"
			return res
		case m := <-c.notifications:
			var p struct {
				TurnID string          `json:"turnId"`
				Item   json.RawMessage `json:"item"`
				Turn   struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(m.Params, &p)
			switch m.Method {
			case "item/completed":
				if p.TurnID != turnID {
					continue
				}
				var item struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				_ = json.Unmarshal(p.Item, &item)
				res.Items = append(res.Items, item.Type)
				if item.Type == "agentMessage" {
					res.Text += item.Text
				}
			case "turn/completed":
				if p.Turn.ID == turnID {
					res.Status = p.Turn.Status
					return res
				}
			}
		}
	}
}

func collectUntil(ctx context.Context, c *client, stop string, timeout time.Duration) []string {
	var seen []string
	deadline := time.After(timeout)
	for {
		select {
		case <-ctx.Done():
			return append(seen, "ctx-done")
		case <-deadline:
			return append(seen, "timeout")
		case m := <-c.notifications:
			entry := m.Method
			if strings.HasPrefix(m.Method, "item/") {
				var p struct {
					Item struct {
						Type string `json:"type"`
					} `json:"item"`
				}
				_ = json.Unmarshal(m.Params, &p)
				entry += ":" + p.Item.Type
			}
			if m.Method != "item/agentMessage/delta" && !strings.Contains(m.Method, "tokenUsage") {
				seen = append(seen, entry)
			}
			if m.Method == stop {
				return seen
			}
		}
	}
}

// summarizeLog reduces the probe log to what the findings need.
func summarizeLog(path string) []map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return []map[string]any{{"error": err.Error()}}
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		e := map[string]any{"kind": r.Kind, "tend_addon": r.Env["TEND_ADDON"], "env_keys": envKeys(r.Env)}
		switch r.Kind {
		case "hook":
			var in map[string]any
			_ = json.Unmarshal(r.Input, &in)
			e["event"], e["source"], e["session_id"], e["token"] = in["hook_event_name"], in["source"], in["session_id"], r.Token
		case "mcp_message":
			var m struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(r.Input, &m)
			e["method"] = m.Method
			if m.Method == "tools/call" {
				e["params"] = json.RawMessage(m.Params)
			}
		}
		out = append(out, e)
	}
	return out
}
