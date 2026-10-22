package traex

import (
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

var _ harness.HistoricalReleaser = (*Workers)(nil)

// IsLegacyThreadID recognizes the canonical native UUID recorded by legacy
// app-server workers. External-terminal workers journal structured JSON instead.
// Unknown identifiers require explicit origin metadata or manual reconciliation.
func IsLegacyThreadID(thread string) bool {
	if len(thread) != 36 {
		return false
	}
	for i, c := range thread {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ReconcileCompletedSession proves the app-server lifecycle, without starting or
// resuming a session. Callers must establish app-server origin, local completion
// and a successful final report. Its worker closes the per-run session (including
// the process group) before Wait returns; a persisted thread is only a rollout ID.
func ReconcileCompletedSession(ctx context.Context, thread string, held bool, completedAt int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if held {
		return fmt.Errorf("%w: historical app-server run is held", harness.ErrHeld)
	}
	if completedAt <= 0 {
		return errors.New("traex: historical completion is unknown")
	}
	if thread == "" {
		return errors.New("traex: historical native thread ID is missing")
	}
	for _, c := range thread {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("traex: historical native thread format is unknown")
		}
	}
	return nil
}

// ReleaseHistorical implements the historical session proof for callers which
// have already established successful app-server completion and native origin.
func (*Workers) ReleaseHistorical(ctx context.Context, _ string, thread string, held bool, completedAt int64) error {
	return ReconcileCompletedSession(ctx, thread, held, completedAt)
}
