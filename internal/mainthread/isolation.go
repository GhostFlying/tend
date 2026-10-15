package mainthread

// Isolation turns off the harness features the main agent must not use:
// shell and patch tools, browsing, plugins, apps, skills and memories
// (spike A5). Sub-agents stay available. Keys are dotted configuration
// paths, the form of `-c key=value`; the model catalog still adds a few
// tools these cannot remove, which is acceptable since the main agent
// changes nothing except through tend's tools.
var Isolation = map[string]any{
	"features.shell_tool":                           false,
	"features.unified_exec":                         false,
	"features.shell_snapshot":                       false,
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
	"web_search":                                    "disabled",
	"tools.update_plan.enabled":                     false,
	"tools.experimental_request_user_input.enabled": false,
	"features.view_image":                           false,
	"features.code_mode":                            false,
	"features.code_mode_only":                       false,
}
