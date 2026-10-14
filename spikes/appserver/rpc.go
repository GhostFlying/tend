package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// message is the JSON-RPC lite envelope used by the app-server over stdio:
// newline-delimited JSON without the "jsonrpc" field.
type message struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s %s", e.Code, e.Message, string(e.Data))
}

// serverRequestHandler answers requests initiated by the app-server, such as
// dynamic tool calls and approvals.
type serverRequestHandler func(method string, params json.RawMessage) (any, error)

// client drives one app-server process.
type client struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	transcript *transcript

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message

	notifications chan message
	onRequest     serverRequestHandler
	done          chan struct{}
}

func startClient(ctx context.Context, bin string, args []string, tr *transcript, onRequest serverRequestHandler) (*client, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = tr.stderrWriter()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{
		cmd:           cmd,
		stdin:         stdin,
		transcript:    tr,
		pending:       map[int64]chan message{},
		notifications: make(chan message, 4096),
		onRequest:     onRequest,
		done:          make(chan struct{}),
	}
	go c.readLoop(stdout)
	return c, nil
}

func (c *client) readLoop(r io.Reader) {
	defer close(c.done)
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			c.transcript.record("in", line)
			var m message
			if jerr := json.Unmarshal(line, &m); jerr != nil {
				c.transcript.note("unparseable line: %v", jerr)
			} else {
				c.dispatch(m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *client) dispatch(m message) {
	switch {
	case m.ID != nil && m.Method != "":
		go c.answer(m)
	case m.ID != nil:
		var id int64
		if err := json.Unmarshal(*m.ID, &id); err != nil {
			c.transcript.note("non-numeric response id %s", string(*m.ID))
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	default:
		c.notifications <- m
	}
}

func (c *client) answer(m message) {
	result, err := c.onRequest(m.Method, m.Params)
	reply := map[string]any{"id": m.ID}
	if err != nil {
		reply["error"] = map[string]any{"code": -32000, "message": err.Error()}
	} else {
		reply["result"] = result
	}
	if werr := c.write(reply); werr != nil {
		c.transcript.note("write reply: %v", werr)
	}
}

func (c *client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.transcript.record("out", b)
	_, err = c.stdin.Write(b)
	return err
}

// call sends a request and waits for its response.
func (c *client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-c.done:
		return fmt.Errorf("%s: app-server exited", method)
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

func (c *client) notify(method string, params any) error {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	return c.write(m)
}

func (c *client) close() {
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

// transcript records every line exchanged with the app-server.
type transcript struct {
	mu sync.Mutex
	f  *os.File
}

func newTranscript(path string) (*transcript, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &transcript{f: f}, nil
}

func (t *transcript) record(dir string, line []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.f, "{\"t\":%q,\"dir\":%q,\"msg\":%s}\n", time.Now().Format(time.RFC3339Nano), dir, trimNewline(line))
}

func (t *transcript) note(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, _ := json.Marshal(fmt.Sprintf(format, args...))
	fmt.Fprintf(t.f, "{\"t\":%q,\"dir\":\"note\",\"msg\":%s}\n", time.Now().Format(time.RFC3339Nano), b)
}

func (t *transcript) stderrWriter() io.Writer {
	pr, pw := io.Pipe()
	go func() {
		s := bufio.NewScanner(pr)
		s.Buffer(make([]byte, 1<<20), 1<<20)
		for s.Scan() {
			t.note("stderr: %s", s.Text())
		}
	}()
	return pw
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}
