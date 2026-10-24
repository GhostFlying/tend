package mainthread

import "github.com/GhostFlying/tend/internal/harness"

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
