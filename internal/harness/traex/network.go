package traex

import (
	"maps"

	"github.com/GhostFlying/tend/internal/harness"
)

// The turn protocol can enable network access independently of filesystem mode.
// Legacy read-only configuration otherwise ignores workspace network settings.
func (t *thread) setNetworkPolicy(cfg harness.ThreadConfig, res ThreadStartResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.networkPolicy = nil
	if !cfg.NetworkAccess {
		return
	}
	// Preserve every resolved filesystem setting, including temp exclusions and
	// native fields introduced after this client. Only networking is changed.
	switch res.Sandbox["type"] {
	case "readOnly", "workspaceWrite":
		t.networkPolicy = maps.Clone(res.Sandbox)
		t.networkPolicy["networkAccess"] = true
	}
}

func (t *thread) turnNetworkPolicy() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.networkPolicy
}
