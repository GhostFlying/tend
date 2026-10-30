package coordination

// Isolation returns a fresh native execution policy for coordinator sessions.
// CLI hooks and effective read-only permissions are independently verified.
func Isolation() map[string]any {
	return map[string]any{
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
}
