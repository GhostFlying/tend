package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// mcpVersion is the protocol version offered when the client asks for
// none.
const mcpVersion = "2025-06-18"

// State is what the agent's MCP server needs to reach its run, written
// next to the worktree's git metadata (see Install).
type State struct {
	// Run is the run the state belongs to. A later run in the same
	// worktree writes its own state over it.
	Run    string `json:"run"`
	Socket string `json:"socket"`
	Token  string `json:"token"`
}

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

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// mcpServer forwards MCP requests on stdio to a run's socket.
type pendingCall struct {
	cancel        context.CancelFunc
	ownerQuestion bool
}

type mcpServer struct {
	callsMu sync.Mutex
	calls   map[string]pendingCall
	state   string // path of the State file
	run     string // the run the agent was started for

	mu  sync.Mutex // guards out
	out *json.Encoder
}

// ServeMCP serves MCP on in and out until in closes or ctx ends:
// `tend-runner worker-mcp --state <file> --run <run>`. The state file is
// read on every request, so the run can move to another socket, for
// example after the runner restarts, while the agent keeps this server.
// Once the file belongs to another run, or to none, requests fail: an
// agent left over from an earlier run never reaches a later one.
func ServeMCP(ctx context.Context, state, run string, in io.Reader, out io.Writer) error {
	s := &mcpServer{state: state, run: run, out: json.NewEncoder(out), calls: make(map[string]pendingCall)}
	if closer, ok := in.(io.ReadCloser); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
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
	// EOF means the native client no longer waits for human answers. Ordinary
	// finite calls still finish so a piped MCP request retains its response.
	defer s.cancelQuestions()
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
			s.send(rpcMessage{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if m.Method == "notifications/cancelled" {
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				s.cancelCall(string(p.RequestID))
			}
			continue
		}
		if len(m.ID) == 0 || m.Method == "" {
			continue // a notification, or an answer we never asked for
		}
		if m.Method == "tools/call" {
			callCtx, cancel := context.WithCancel(ctx)
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(m.Params, &p)
			s.callsMu.Lock()
			s.calls[string(m.ID)] = pendingCall{cancel: cancel, ownerQuestion: p.Name == harness.AskOwnerTool}
			s.callsMu.Unlock()
			wg.Go(func() {
				defer cancel()
				defer func() { s.callsMu.Lock(); delete(s.calls, string(m.ID)); s.callsMu.Unlock() }()
				s.reply(m, s.call(callCtx, m))
			})
			continue
		}
		s.reply(m, s.handle(ctx, m))
	}
}

func (s *mcpServer) cancelCall(id string) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	if call, ok := s.calls[id]; ok {
		call.cancel()
	}
}

func (s *mcpServer) cancelQuestions() {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	for _, call := range s.calls {
		if call.ownerQuestion {
			call.cancel()
		}
	}
}

func (s *mcpServer) send(m rpcMessage) {
	m.JSONRPC = "2.0"
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.out.Encode(m)
}

func (s *mcpServer) reply(req rpcMessage, res any) {
	if re, ok := res.(*rpcError); ok {
		s.send(rpcMessage{ID: req.ID, Error: re})
		return
	}
	s.send(rpcMessage{ID: req.ID, Result: res})
}

func (s *mcpServer) handle(ctx context.Context, m rpcMessage) any {
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
			"serverInfo":      map[string]any{"name": "tend-worker", "version": "1"},
		}
	case "ping":
		return map[string]any{}
	case "tools/list":
		res, err := s.do(ctx, request{Method: "list"})
		if err != nil {
			return &rpcError{Code: -32603, Message: err.Error()}
		}
		tools := []mcpTool{}
		for _, t := range res.Tools {
			tools = append(tools, mcpTool(t))
		}
		return map[string]any{"tools": tools}
	}
	return &rpcError{Code: -32601, Message: "method not found: " + m.Method}
}

func (s *mcpServer) call(ctx context.Context, m rpcMessage) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
		return &rpcError{Code: -32602, Message: "tools/call needs a name"}
	}
	req := request{Method: "call", Name: p.Name, CallID: string(m.ID), Arguments: p.Arguments}
	res, err := s.do(ctx, req)
	for err != nil && p.Name == harness.AskOwnerTool && ctx.Err() == nil {
		var transport *transportError
		if !errors.As(err, &transport) && !errors.Is(err, os.ErrNotExist) {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return callResult{Content: []content{{Type: "text", Text: ctx.Err().Error()}}, IsError: true}
		case <-timer.C:
		}
		// Reload the state each time: reconnect only while the original run
		// still owns the marker, and use its current socket credentials.
		res, err = s.do(ctx, req)
	}
	if err != nil {
		return callResult{Content: []content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	return callResult{Content: []content{{Type: "text", Text: res.Text}}, IsError: !res.Success}
}

func (s *mcpServer) do(ctx context.Context, req request) (response, error) {
	b, err := os.ReadFile(s.state)
	if err != nil {
		return response{}, fmt.Errorf("bridge: this worktree has no run: %w", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return response{}, fmt.Errorf("bridge: read the run's state: %w", err)
	}
	if st.Run != s.run {
		return response{}, errors.New("bridge: this worktree has moved on to another run; this agent's run has ended")
	}
	req.Token = st.Token
	return do(ctx, st.Socket, req)
}
