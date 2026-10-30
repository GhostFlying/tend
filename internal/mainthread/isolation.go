package mainthread

import (
	"maps"

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

// backgroundThreadConfig implements D111's temporary native auto-mode policy.
// Tend's background tool scope is enforced separately by the tool service.
func backgroundThreadConfig(cfg harness.ThreadConfig) harness.ThreadConfig {
	cfg.NetworkAccess = true
	cfg.Sandbox = "workspace-write"
	cfg.ApprovalPolicy = "on-request"
	cfg.ApprovalsReviewer = "auto_review"
	cfg.Config = maps.Clone(cfg.Config)
	if cfg.Config == nil {
		cfg.Config = make(map[string]any)
	}
	// The main dispatcher's tool feature switches are not inherited by a
	// background session. Native tools remain available under auto review.
	for key := range Isolation {
		delete(cfg.Config, key)
	}
	// The runtime rejects simultaneous permission_mode and sandbox overrides.
	// Explicit protocol fields above select the auto-mode permission tuple.
	delete(cfg.Config, "permission_mode")
	cfg.Config["sandbox_workspace_write.writable_roots"] = []string{}
	return cfg
}
