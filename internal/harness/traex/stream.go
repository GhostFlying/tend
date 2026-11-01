package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/GhostFlying/tend/internal/harness"
)

// AttachUnstartedThread rebinds only the tool handler of the original live
// registration before its first turn. It keeps the accepted native settings;
// unlike resume it neither loads history nor creates a replacement thread.
func (s *Session) AttachUnstartedThread(ctx context.Context, id string, handle harness.ToolHandler) (harness.Thread, error) {
	unlock, err := s.lockThreadLifecycle(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	t := s.thread(id)
	if t == nil {
		return nil, fmt.Errorf("%w: original unstarted thread is unavailable", harness.ErrRejected)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started || t.ended || t.released {
		return nil, fmt.Errorf("%w: thread is no longer an unstarted registration", harness.ErrRejected)
	}
	t.onTool = handle
	return t, nil
}

// InjectUser records immutable task context without starting a model turn.
func (s *Session) InjectUser(ctx context.Context, threadID, text string) error {
	item, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}})
	if err != nil {
		return fmt.Errorf("traex: encode user context: %w", err)
	}
	return s.conn.call(ctx, methodThreadInject, ThreadInjectItemsParams{ThreadID: threadID, Items: []json.RawMessage{item}}, nil)
}

// Connect completes the native handshake on a caller-owned JSON-line transport.
// Closing the session closes the transport; it does not claim remote stop proof.
func Connect(ctx context.Context, transport io.ReadWriteCloser, version string, log *slog.Logger) (*Session, error) {
	s := newSession(transport, transport, log)
	if err := s.initialize(ctx, version); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("traex: connect: %w", err)
	}
	return s, nil
}

// ThreadIdle observes native activity before handing an existing conversation
// back to automation. A missing or unloaded thread is not live idle proof.
func (s *Session) ThreadIdle(ctx context.Context, id string) (bool, error) {
	var result struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	err := s.conn.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &result)
	return result.Thread.Status.Type == "idle", err
}
