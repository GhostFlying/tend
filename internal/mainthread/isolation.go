package mainthread

import (
	"maps"

	"github.com/GhostFlying/tend/internal/harness"
)

// ThreadConfig gives the hosted dispatcher only tend coordination tools and
// read-only presentation. Investigation and execution belong to workers (D88).
// An empty model leaves the harness default.
func ThreadConfig(cwd, model string) harness.ThreadConfig {
	return harness.ThreadConfig{
		Cwd: cwd, Model: model, DeveloperInstructions: Instructions,
		Sandbox: "read-only", ApprovalPolicy: "never",
		Config: Isolation,
	}
}

// Isolation disables native execution and independent research paths so the
// hosted dispatcher delegates them through tend. Images and bounded ledger/file
// reads remain useful for understanding the owner. Keys are dotted harness
// configuration paths, also applied when a thread resumes or hands off.
var Isolation = map[string]any{
	"features.shell_tool":                           false,
	"features.js_repl":                              false,
	"features.multi_agent":                          false,
	"features.multi_agent_v2":                       false,
	"web_search":                                    "disabled",
	"sandbox_workspace_write.writable_roots":        []string{},
	"features.apply_patch_freeform":                 false,
	"features.code_mode_host":                       false,
	"features.browser_use":                          false,
	"features.browser_use_external":                 false,
	"features.in_app_browser":                       false,
	"features.computer_use":                         false,
	"features.image_generation":                     false,
	"features.plugins":                              false,
	"features.apps":                                 false,
	"features.memories":                             false,
	"features.goals":                                false,
	"features.skill_search":                         false,
	"features.tool_suggest":                         false,
	"features.workspace_dependencies":               false,
	"features.workspace_undo":                       false,
	"skills.bundled.enabled":                        false,
	"skills.include_instructions":                   false,
	"tools.update_plan.enabled":                     false,
	"tools.experimental_request_user_input.enabled": false,
	"features.code_mode":                            false,
	"features.code_mode_only":                       false,
}

// backgroundThreadConfig implements D111's temporary native auto-mode policy.
// Tend's background tool scope is enforced separately by the tool service.
func backgroundThreadConfig(cfg harness.ThreadConfig) harness.ThreadConfig {
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
