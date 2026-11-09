package traex

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
)

func (*thread) ConfirmUsageWriterStopped(ctx context.Context, writer string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var original ownedProcessIdentity
	if err := json.Unmarshal([]byte(writer), &original); err != nil {
		return err
	}
	return confirmOwnedProcessStopped(original)
}

func (t *thread) UsageSource() nativeusage.Source {
	t.mu.Lock()
	defer t.mu.Unlock()
	provider := "traex"
	if t.s.codex {
		provider = "codex"
	}
	return nativeusage.Source{NoDispatchProven: t.newlyCreated && !t.started, WriterID: t.s.usageWriterID, Path: t.rolloutPath, Provider: provider, ThreadID: t.id, SessionID: t.nativeSessionID}
}
