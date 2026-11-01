// Package remote connects centrally hosted native loops to runner-owned execution.
package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

const Path = "/runner/v1/native/"

// Hello pins a connection to the workspace already prepared for its run.
type Hello struct {
	Cwd      string `json:"cwd"`
	ThreadID string `json:"thread_id,omitempty"`
}

// Dial opens an authenticated run transport. Native messages remain JSON frames.
func Dial(ctx context.Context, client *http.Client, base, run, token, channel string) (io.ReadWriteCloser, error) {
	base = strings.TrimRight(base, "/")
	if strings.HasPrefix(base, "https://") {
		base = "wss://" + strings.TrimPrefix(base, "https://")
	} else if strings.HasPrefix(base, "http://") {
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	c, _, err := websocket.Dial(ctx, base+Path+channel, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": {"Bearer " + token}, "X-Tend-Run": {run}}})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(32 << 20)
	return stream(c), nil
}

type frameStream struct {
	c       *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	pending []byte
	read    sync.Mutex
}

func stream(c *websocket.Conn) *frameStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &frameStream{c: c, ctx: ctx, cancel: cancel}
}
func (s *frameStream) Read(p []byte) (int, error) {
	s.read.Lock()
	defer s.read.Unlock()
	if len(s.pending) == 0 {
		_, b, e := s.c.Read(s.ctx)
		if e != nil {
			return 0, e
		}
		s.pending = append(b, '\n')
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
func (s *frameStream) Write(p []byte) (int, error) {
	e := s.c.Write(s.ctx, websocket.MessageText, []byte(strings.TrimSuffix(string(p), "\n")))
	if e != nil {
		return 0, e
	}
	return len(p), nil
}
func (s *frameStream) Close() error { s.cancel(); return s.c.CloseNow() }
func writeFrame(ctx context.Context, c *websocket.Conn, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.Write(ctx, websocket.MessageText, b)
}
