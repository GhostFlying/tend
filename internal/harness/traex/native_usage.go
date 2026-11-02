package traex

import "github.com/GhostFlying/tend/internal/harness/nativeusage"

func (t *thread) UsageSource() nativeusage.Source {
	t.mu.Lock()
	defer t.mu.Unlock()
	provider := "traex"
	if t.s.codex {
		provider = "codex"
	}
	return nativeusage.Source{Path: t.rolloutPath, Provider: provider, ThreadID: t.id, SessionID: t.nativeSessionID}
}
