package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// The MCP bridge (D46) implements the part of the Model Context Protocol
// that stdio tool servers need: initialize, tools/list and tools/call,
// with JSON-RPC messages one per line. Tool definitions come from the
// server and calls are forwarded to it.

// mcpVersion is the protocol version offered when the client asks for
// none.
const mcpVersion = "2025-06-18"

// callAttempts bounds how often a tool call is sent when tend cannot be
// reached; every attempt carries the same request ID (D48).
const callAttempts = 3

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpTool is a tool as MCP lists it.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type bridge struct {
	e     *Env
	agent protocol.AgentKind
	off   bool
	retry time.Duration

	mu  sync.Mutex // guards out
	out *json.Encoder
}

// runMCP serves MCP on stdio until stdin closes: `tend mcp --agent <a>`.
func runMCP(ctx context.Context, e *Env, args []string) int {
	fs := newFlags(e, "mcp")
	agent := fs.String("agent", "", "the agent this server runs for: claude, codex or traex")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 || !protocol.AgentKind(*agent).Valid() {
		fmt.Fprintln(e.Stderr, "usage: tend mcp --agent <claude|codex|traex>")
		return exitUsage
	}
	b := &bridge{e: e, agent: protocol.AgentKind(*agent), off: e.addonOff(), retry: 500 * time.Millisecond, out: json.NewEncoder(e.Stdout)}
	if err := b.serve(ctx, e.Stdin); err != nil {
		fmt.Fprintf(e.Stderr, "tend mcp: %v\n", err)
		return exitError
	}
	return exitOK
}

// serve answers requests until stdin closes or ctx ends, then waits for
// the calls in flight, which end with ctx.
func (b *bridge) serve(ctx context.Context, in io.Reader) error {
	// The reader runs on its own so that cancellation does not wait for
	// stdin; when ctx ends first it blocks only until the process exits.
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			select {
			case lines <- bytes.Clone(sc.Bytes()):
			case <-ctx.Done():
				readErr <- nil
				return
			}
		}
		readErr <- sc.Err()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		var line []byte
		select {
		case <-ctx.Done():
			return nil
		case l, ok := <-lines:
			if !ok {
				return <-readErr
			}
			line = l
		}
		var m rpcMessage
		if err := json.Unmarshal(line, &m); err != nil {
			b.send(rpcMessage{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(m.ID) == 0 || m.Method == "" {
			continue // a notification, or an answer we never asked for
		}
		if m.Method == "tools/call" {
			// A slow call must not hold up others or pings.
			wg.Go(func() { b.reply(m, b.call(ctx, m.Params)) })
			continue
		}
		b.reply(m, b.handle(ctx, m))
	}
}

func (b *bridge) send(m rpcMessage) {
	m.JSONRPC = "2.0"
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.out.Encode(m)
}

// reply answers a request with a result or, for an *rpcError, an error.
func (b *bridge) reply(req rpcMessage, res any) {
	if re, ok := res.(*rpcError); ok {
		b.send(rpcMessage{ID: req.ID, Error: re})
		return
	}
	b.send(rpcMessage{ID: req.ID, Result: res})
}

func (b *bridge) handle(ctx context.Context, m rpcMessage) any {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = mcpVersion
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "tend", "version": "1"},
		}
	case "ping":
		return map[string]any{}
	case "tools/list":
		return b.list(ctx)
	}
	return &rpcError{Code: -32601, Message: "method not found: " + m.Method}
}

func (b *bridge) list(ctx context.Context) any {
	tools := []mcpTool{}
	if b.off {
		return map[string]any{"tools": tools}
	}
	c, err := b.e.client(protocol.ScopeAgent)
	if err != nil {
		return &rpcError{Code: -32603, Message: err.Error()}
	}
	var specs []protocol.ToolSpec
	if err := c.do(ctx, "GET", "/api/v1/agent/tools", nil, &specs); err != nil {
		return &rpcError{Code: -32603, Message: err.Error()}
	}
	for _, s := range specs {
		tools = append(tools, mcpTool{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema})
	}
	return map[string]any{"tools": tools}
}

// toolResult is the answer to tools/call. Failures tend reports go back to
// the model as tool results, not as protocol errors, so it can act on
// them.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}

func (b *bridge) call(ctx context.Context, params json.RawMessage) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			Turn struct {
				SessionID string `json:"session_id"`
			} `json:"x-codex-turn-metadata"`
			SessionID string `json:"sessionId"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return &rpcError{Code: -32602, Message: "tools/call needs a tool name"}
	}
	if b.off {
		return toolResult("tend is turned off in this environment", true)
	}
	c, err := b.e.client(protocol.ScopeAgent)
	if err != nil {
		return toolResult(err.Error(), true)
	}
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage("{}")
	}
	req := protocol.ToolRequest{RequestID: b.e.IDs.New(protocol.PrefixRequest), Agent: b.agent, SessionID: b.session(p.Meta.Turn.SessionID, p.Meta.SessionID), Arguments: p.Arguments}
	var res protocol.ToolResponse
	for attempt := 1; ; attempt++ {
		err = c.do(ctx, "POST", "/api/v1/agent/tools/"+url.PathEscape(p.Name), req, &res)
		if !isTransport(err) || attempt == callAttempts || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(b.retry):
		}
	}
	var pe *protocol.Error
	switch {
	case err == nil:
		return toolResult(res.Content, !res.Success)
	case errors.As(err, &pe):
		body, _ := json.Marshal(map[string]any{"ok": false, "error": pe})
		return toolResult(string(body), true)
	default:
		// The call may or may not have reached tend.
		return toolResult(fmt.Sprintf("%v. The call may still have taken effect; check with requests_read (request %s) before repeating it.", err, req.RequestID), true)
	}
}

// session finds the agent session a call belongs to (C5): Claude Code
// starts its MCP servers per session and says which in the environment;
// Codex and traex name it in every call's metadata.
func (b *bridge) session(fromTurn, fromMeta string) string {
	if b.agent == protocol.AgentClaude {
		return b.e.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	if fromTurn != "" {
		return fromTurn
	}
	return fromMeta
}
