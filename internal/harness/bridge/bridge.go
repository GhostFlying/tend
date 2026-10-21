// Package bridge lets an agent TUI that tend does not drive through an
// app-server call a run's tools (D66). The runner serves the tools on a
// local socket, and `tend-runner worker-mcp` is the agent's MCP server,
// forwarding to that socket. The tools are answered in the runner's own
// process, as an app-server worker's are, so the run's result never
// leaves the runner on another path.
package bridge

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// request is one call on the socket, one per connection.
type request struct {
	Token     string          `json:"token"`
	Method    string          `json:"method"` // list or call
	Name      string          `json:"name,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type response struct {
	Tools   []tool `json:"tools,omitempty"`
	Success bool   `json:"success,omitempty"`
	Text    string `json:"text,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Server serves a run's tools on a unix socket.
type Server struct {
	ln     net.Listener
	path   string
	token  string
	tools  []harness.ToolSpec
	handle harness.ToolHandler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Listen serves tools at path, a socket only the runner's user can open,
// for requests that carry token. A file left at path by an earlier
// attempt is replaced.
func Listen(path, token string, tools []harness.ToolSpec, handle harness.ToolHandler) (*Server, error) {
	if token == "" {
		return nil, errors.New("bridge: a token is required")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("bridge: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("bridge: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("bridge: restrict socket: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, path: path, token: token, tools: tools, handle: handle, ctx: ctx, cancel: cancel}
	s.wg.Go(s.accept)
	return s, nil
}

// Close stops serving, waits for calls in flight and removes the socket.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.cancel()
	s.wg.Wait()
	_ = os.Remove(s.path)
	return err
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Go(func() {
			// Close unblocks a client that neither sends nor reads.
			stop := context.AfterFunc(s.ctx, func() { conn.Close() })
			defer stop()
			defer conn.Close()
			s.serve(conn)
		})
	}
}

func (s *Server) serve(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var req request
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &req) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	res := s.answer(req)
	b, _ := json.Marshal(res)
	_, _ = conn.Write(append(b, '\n'))
}

func (s *Server) answer(req request) response {
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.token)) != 1 {
		return response{Error: "this run's token is required"}
	}
	switch req.Method {
	case "list":
		res := response{Tools: []tool{}}
		for _, t := range s.tools {
			res.Tools = append(res.Tools, tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		return res
	case "call":
		known := false
		for _, t := range s.tools {
			known = known || t.Name == req.Name
		}
		if !known {
			return response{Text: "there is no tool " + req.Name}
		}
		r := s.handle(s.ctx, harness.ToolCall{CallID: req.CallID, Tool: req.Name, Arguments: req.Arguments})
		return response{Success: r.Success, Text: r.Text}
	}
	return response{Error: "unknown method " + req.Method}
}

// do sends one request to the socket at path.
func do(ctx context.Context, path string, req request) (response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return response{}, fmt.Errorf("bridge: the run's runner is not reachable: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	b, err := json.Marshal(req)
	if err != nil {
		return response{}, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return response{}, fmt.Errorf("bridge: send: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return response{}, fmt.Errorf("bridge: receive: %w", err)
	}
	var res response
	if err := json.Unmarshal(line, &res); err != nil {
		return response{}, fmt.Errorf("bridge: decode: %w", err)
	}
	if res.Error != "" {
		return response{}, errors.New("bridge: " + res.Error)
	}
	return res, nil
}
