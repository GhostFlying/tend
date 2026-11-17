package coordination

import (
	"context"
	"github.com/GhostFlying/tend/internal/harness"
)

// ReadThreadHistory creates a non-executing view. It installs no callbacks and
// does not resume the executor with the current session's changed tool menu.
func (s *Session) ReadThreadHistory(_ context.Context, id string) (harness.Thread, error) {
	return &historyThread{session: s, id: id}, nil
}

type historyThread struct {
	session *Session
	id      string
}

func (t *historyThread) ID() string                                     { return t.id }
func (*historyThread) Events() <-chan harness.Event                     { return nil }
func (*historyThread) InjectDeveloper(context.Context, ...string) error { return harness.ErrRejected }
func (*historyThread) StartTurn(context.Context, harness.TurnInput) (string, error) {
	return "", harness.ErrRejected
}
func (*historyThread) Interrupt(context.Context, string) error { return harness.ErrRejected }
func (t *historyThread) FindTurn(ctx context.Context, message, id string) (harness.TurnReceipt, bool, error) {
	var result FindResult
	err := t.session.call(ctx, "find", Request{ThreadID: t.id, MessageID: message, TurnID: id}, &result)
	return result.Receipt, result.Found, err
}
