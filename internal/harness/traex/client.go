package traex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// message is the app-server's wire format: newline-delimited JSON-RPC
// without the "jsonrpc" field. Requests carry id and method, responses id
// and result or error, notifications only method.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError is an error response from the app-server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("app-server error %d: %s", e.Code, e.Message) }

// ErrClosed is returned for calls on a connection whose app-server exited.
var ErrClosed = errors.New("traex: app-server connection closed")

// handlers receive what the server initiates.
type handlers struct {
	// request answers a server request; the result is sent back as is.
	request func(ctx context.Context, method string, params json.RawMessage) (any, error)
	// notify receives a notification.
	notify func(method string, params json.RawMessage)
}

// conn is one JSON-RPC connection to an app-server. A single goroutine
// writes, so a caller waiting to send can give up when its context ends
// even if the app-server stopped reading.
type conn struct {
	w      io.WriteCloser
	h      handlers
	log    *slog.Logger
	outbox chan []byte

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

func newConn(r io.Reader, w io.WriteCloser, h handlers, log *slog.Logger) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{w: w, h: h, log: log, outbox: make(chan []byte), pending: map[int64]chan message{}, done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go c.read(r)
	go c.writeLoop()
	return c
}

func (c *conn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case b := <-c.outbox:
			if _, err := c.w.Write(b); err != nil {
				c.log.Warn("app-server write failed", "err", err)
				_ = c.w.Close()
				return
			}
		}
	}
}

func (c *conn) read(r io.Reader) {
	defer func() {
		c.cancel()
		close(c.done)
	}()
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var m message
			if jerr := json.Unmarshal(line, &m); jerr != nil {
				c.log.Warn("unparseable app-server line", "err", jerr)
			} else {
				c.dispatch(m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *conn) dispatch(m message) {
	switch {
	case m.ID != nil && m.Method != "":
		go c.answer(m)
	case m.ID != nil:
		var id int64
		if err := json.Unmarshal(m.ID, &id); err != nil {
			c.log.Warn("app-server response with a foreign id", "id", string(m.ID))
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
		c.h.notify(m.Method, m.Params)
	}
}

func (c *conn) answer(m message) {
	result, err := c.h.request(c.ctx, m.Method, m.Params)
	reply := map[string]any{"id": m.ID}
	if err != nil {
		code := -32603
		if errors.Is(err, errUnknownMethod) {
			code = -32601
		}
		reply["error"] = map[string]any{"code": code, "message": err.Error()}
	} else {
		reply["result"] = result
	}
	if err := c.write(c.ctx, reply); err != nil {
		c.log.Warn("could not answer an app-server request", "method", m.Method, "err", err)
	}
}

var errUnknownMethod = errors.New("traex: method not handled")

func (c *conn) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("traex: encode: %w", err)
	}
	select {
	case c.outbox <- append(b, '\n'):
		return nil
	case <-c.done:
		return ErrClosed
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// call sends a request and decodes its result into out.
func (c *conn) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: %w", method, m.Error)
		}
		if out != nil {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("%s: decode result: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		return fmt.Errorf("%s: %w", method, ErrClosed)
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", method, context.Cause(ctx))
	}
}

func (c *conn) notify(ctx context.Context, method string, params any) error {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	return c.write(ctx, m)
}
