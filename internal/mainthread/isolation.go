package mainthread

import (
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
)

// ThreadConfig adds Tend tools without overriding the selected runtime/device
// capability policy. An empty model leaves the harness default.
func ThreadConfig(cwd, model string) harness.ThreadConfig {
	return harness.ThreadConfig{
		Cwd: cwd, Model: model, DeveloperInstructions: Instructions, NetworkAccess: true,
		Config: map[string]any{},
	}
}

// Isolation is reserved for receipt-only recovery of already closing work.
// Normal Agent roles inherit their selected capability and runtime policy.
var Isolation = coordination.Isolation()
