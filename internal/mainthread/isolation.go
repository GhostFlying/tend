package mainthread

import "github.com/GhostFlying/tend/internal/harness"

// ThreadConfig is the front thread's configuration in cwd (D68). The
// sandbox lets the main agent read anything, such as the repositories it
// plans work for, and write only in cwd; what needs more is asked for on
// request and reviewed automatically, since nobody watches the thread to
// approve it. An empty model leaves the harness default.
func ThreadConfig(cwd, model string) harness.ThreadConfig {
	return harness.ThreadConfig{
		Cwd: cwd, Model: model, DeveloperInstructions: Instructions,
		Sandbox: "workspace-write", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review",
		Config: Isolation,
	}
}

// Isolation turns off the harness features the main agent must not use:
// editing, browsers and computer use, image generation, plugins, apps,
// skills and memories, which would act outside tend or bring in the
// owner's own setup, and asking the user, which no client answers. The
// shell stays so the main agent can read repositories (D68). Keys are
// dotted configuration paths, the form of `-c key=value`.
var Isolation = map[string]any{
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
