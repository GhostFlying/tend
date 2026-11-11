package traex

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// ReadThreadHistory uses the native read API, never thread/resume. Even a
// read-only resume can persist policy changes in a reusable business thread.
func (s *Session) ReadThreadHistory(ctx context.Context, id string) (harness.Thread, error) {
	var res struct {
		Thread Thread `json:"thread"`
	}
	if id == "" {
		return nil, fmt.Errorf("%w: history thread identity is missing", harness.ErrRejected)
	}
	if err := s.conn.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &res); err != nil {
		return nil, err
	}
	if res.Thread.ID != id {
		return nil, fmt.Errorf("%w: native history thread identity differs", harness.ErrRejected)
	}
	path := ""
	if res.Thread.Path != nil {
		path = *res.Thread.Path
	}
	return &historyThread{session: s, id: id, path: path}, nil
}

// A history handle deliberately exposes neither native defaults nor usage
// writer metadata: thread/read did not report those execution facts.
type historyThread struct {
	session *Session
	id      string
	path    string
}

func (t *historyThread) ID() string                 { return t.id }
func (t *historyThread) RolloutPath() string        { return t.path }
func (*historyThread) Events() <-chan harness.Event { return nil }
func (*historyThread) InjectDeveloper(context.Context, ...string) error {
	return fmt.Errorf("%w: history handles cannot inject instructions", harness.ErrRejected)
}
func (*historyThread) StartTurn(context.Context, harness.TurnInput) (string, error) {
	return "", fmt.Errorf("%w: history handles cannot start turns", harness.ErrRejected)
}
func (*historyThread) Interrupt(context.Context, string) error {
	return fmt.Errorf("%w: history handles cannot interrupt turns", harness.ErrRejected)
}
func (t *historyThread) FindTurn(ctx context.Context, identity, turn string) (harness.TurnReceipt, bool, error) {
	return (&thread{s: t.session, id: t.id}).FindTurn(ctx, identity, turn)
}
