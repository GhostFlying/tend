package mainthread

import (
	"context"
	"log/slog"
	"sync"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
)

// sessionDiagnostics freezes only the attachment's known ledger identity.
// Native thread and turn identity may change while a session reconnects; callers
// can join the immutable run to those facts without guessing from cached state.
type sessionDiagnostics struct {
	log     *slog.Logger
	exiting sync.Once
}

func (m *Manager) sessionDiagnostics(selected *hostedconfig.Selection) *sessionDiagnostics {
	log := m.cfg.Log.With("instance", m.cfg.Instance, "conversation_id", m.cfg.ConversationID)
	if selected != nil {
		log = log.With("issue_id", selected.IssueID, "run_id", selected.RunnerRunID,
			"assignment_epoch", selected.AssignmentEpoch)
	}
	return &sessionDiagnostics{log: log}
}

func (d *sessionDiagnostics) exit(ctx context.Context, cause error) {
	d.exiting.Do(func() {
		d.log.InfoContext(ctx, "manager session exiting", "serve_error", cause, "context_cause", context.Cause(ctx))
	})
}

func (d *sessionDiagnostics) cleanup(ctx context.Context, level slog.Level, phase string, err error) {
	d.log.Log(ctx, level, "manager session cleanup requires reconciliation", "cleanup_phase", phase,
		"cleanup_error", err, "context_cause", context.Cause(ctx))
}

func (d *sessionDiagnostics) close(ctx context.Context, s harness.Session, trigger string) error {
	d.log.InfoContext(ctx, "manager session close requested", "close_trigger", trigger, "context_cause", context.Cause(ctx))
	err := s.Close()
	d.log.InfoContext(ctx, "manager session close returned", "close_trigger", trigger,
		"context_cause", context.Cause(ctx), "close_error", err)
	return err
}

func (m *Manager) closeSession(ctx context.Context, s harness.Session, request, trigger string) error {
	d := m.sessionDiagnostics(m.namedSelection())
	if request != "" {
		d.log = d.log.With("initiating_request_id", request)
	}
	return d.close(ctx, s, trigger)
}
