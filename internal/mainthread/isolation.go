package mainthread

import (
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
)

// ThreadConfig selects Tend coordination tools and read-only presentation.
// The named hosted adapter also blocks native delegation through its fixed CLI
// hook; native tool schema names alone do not establish isolation (D88/D122).
// An empty model leaves the harness default.
func ThreadConfig(cwd, model string) harness.ThreadConfig {
	return harness.ThreadConfig{
		Cwd: cwd, Model: model, DeveloperInstructions: Instructions, NetworkAccess: true,
		Sandbox: "read-only", ApprovalPolicy: "never",
		Config: Isolation,
	}
}

// Isolation disables native execution and independent research paths so the
// hosted dispatcher delegates them through Tend. Images and bounded material
// presentation remain useful for understanding the owner. Keys are dotted
// harness configuration paths, also applied on resume or handoff.
var Isolation = coordination.Isolation()
