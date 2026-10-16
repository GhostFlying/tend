package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// The probe stands in for `tend hook` and `tend mcp`. Its log path and
// label come from the command line, never from the environment, because
// whether the agent passes its environment on is one of the questions.

// record is one line of the probe log.
type record struct {
	Time   time.Time         `json:"time"`
	Kind   string            `json:"kind"` // hook, mcp_start, mcp_message
	Label  string            `json:"label"`
	PPID   int               `json:"ppid"`
	Env    map[string]string `json:"env,omitempty"`
	Input  json.RawMessage   `json:"input,omitempty"`
	Output string            `json:"output,omitempty"`
	Token  string            `json:"token,omitempty"`
}

var logMu sync.Mutex

func appendLog(path string, r record) {
	logMu.Lock()
	defer logMu.Unlock()
	r.Time = time.Now()
	r.PPID = os.Getppid()
	b, _ := json.Marshal(r)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// interestingEnv keeps the variables that say who started the process.
func interestingEnv() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "TEND_"), strings.HasPrefix(k, "CLAUDE"), strings.HasPrefix(k, "CODEX"),
			strings.HasPrefix(k, "TRAE"), strings.HasPrefix(k, "MCP"), k == "PWD":
			if strings.Contains(strings.ToUpper(k), "TOKEN") || strings.Contains(strings.ToUpper(k), "KEY") {
				v = "<redacted>"
			}
			out[k] = v
		}
	}
	return out
}

func envKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runHook implements: probe hook <log> <label> <mode>.
//
// Modes: claude and codex print the event's JSON output with an
// additionalContext; plain prints the token as plain stdout; sleep waits
// 30s first; fail exits 1; block exits 2; garbage prints invalid JSON;
// bigN prints about N thousand characters of plain context with markers.
func runHook(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: probe hook <log> <label> <mode>")
		return 2
	}
	logPath, label, mode := args[0], args[1], args[2]
	in, _ := io.ReadAll(os.Stdin)
	var event struct {
		HookEventName string `json:"hook_event_name"`
		Source        string `json:"source"`
	}
	_ = json.Unmarshal(in, &event)
	token := fmt.Sprintf("PROBE-%s-%s-%d", label, strings.ToUpper(event.HookEventName), time.Now().UnixNano()%1000000)
	if event.Source != "" {
		token += "-" + strings.ToUpper(event.Source)
	}
	ctx := "Context from the tend probe. " + token
	var out string
	switch mode {
	case "claude", "codex":
		b, _ := json.Marshal(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     event.HookEventName,
				"additionalContext": ctx,
			},
		})
		out = string(b)
	case "plain":
		out = ctx
	case "sleep":
		time.Sleep(30 * time.Second)
		out = ctx
	case "garbage":
		out = "{not json " + token
	default:
		// bigN prints about N thousand characters of plain context with a
		// marker every thousand, to find where the agent truncates.
		var n int
		if _, err := fmt.Sscanf(mode, "big%d", &n); err != nil {
			break
		}
		var b strings.Builder
		b.WriteString(ctx)
		for i := 1; i <= n; i++ {
			b.WriteString(" " + strings.Repeat("lorem ipsum ", 80))
			fmt.Fprintf(&b, " MARK-%dK", i)
		}
		out = b.String()
	}
	raw := json.RawMessage(in)
	if !json.Valid(in) {
		raw, _ = json.Marshal(string(in))
	}
	appendLog(logPath, record{Kind: "hook", Label: label + "/" + mode, Env: interestingEnv(), Input: raw, Output: out, Token: token})
	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "probe hook failing on purpose")
		return 1
	case "block":
		fmt.Fprintln(os.Stderr, "probe hook blocking on purpose")
		return 2
	}
	fmt.Println(out)
	return 0
}

// runMCP implements: probe mcp <log> <label>. It is a minimal stdio MCP
// server with one tool, probe_echo, and logs every message it receives.
func runMCP(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe mcp <log> <label>")
		return 2
	}
	logPath, label := args[0], args[1]
	env := interestingEnv()
	appendLog(logPath, record{Kind: "mcp_start", Label: label, Env: env})
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	w := bufio.NewWriter(os.Stdout)
	reply := func(id json.RawMessage, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		w.Write(append(b, '\n'))
		w.Flush()
	}
	for sc.Scan() {
		line := sc.Bytes()
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		appendLog(logPath, record{Kind: "mcp_message", Label: label, Input: append(json.RawMessage(nil), line...)})
		if len(m.ID) == 0 {
			continue // notification
		}
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(m.Params, &p)
			reply(m.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "probe", "version": "0"},
			})
		case "tools/list":
			reply(m.ID, map[string]any{"tools": []any{map[string]any{
				"name":        "probe_echo",
				"description": "Echo the given text back, with a marker. Use it when asked to call probe_echo.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}}})
		case "tools/call":
			var p struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &p)
			reply(m.ID, map[string]any{"content": []any{map[string]any{
				"type": "text", "text": "probe_echo says: " + p.Arguments.Text + " MCPMARK-" + label,
			}}})
		case "ping":
			reply(m.ID, map[string]any{})
		default:
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			w.Write(append(b, '\n'))
			w.Flush()
		}
	}
	return 0
}
