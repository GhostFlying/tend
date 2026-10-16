package gitws

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// writeAgentsFile writes the workspace root's AGENTS.md (D37). The root is
// not inside any repository, so the harness does not load the mounts' own
// AGENTS.md files; this file lays out the workspace and points to them.
func writeAgentsFile(root string, as protocol.Assignment, st state) error {
	var b strings.Builder
	b.WriteString("# Workspace\n\n")
	b.WriteString("This directory was laid out by tend, not by a repository. Each sub-directory below is a git worktree of one repository.\n\n")
	b.WriteString("| Directory | Repository | Branch | Base commit |\n| --- | --- | --- | --- |\n")
	for _, m := range as.Workspace.Mounts {
		ms := st.Mounts[m.Path]
		branch := ms.Branch
		if branch == "" {
			branch = "(detached)"
		}
		fmt.Fprintf(&b, "| `%s/` | %s | `%s` | `%s` |\n", m.Path, m.Repo, branch, ms.Base)
	}
	b.WriteString("\n## Base\n\n")
	b.WriteString("The task's work starts from each mount's base commit above. There are no local branches besides the task branch, so a check that compares with the base, such as \"the diff against main\", means the base commit. ")
	if as.Role == protocol.RoleAccept {
		// Collect committed everything, new files included.
		b.WriteString("`git diff <base commit>` shows everything the task changed.\n\n")
	} else {
		b.WriteString("`git diff <base commit>` shows the task's changes to tracked files; files added since stay untracked until tend records the run, so `git status` lists them.\n\n")
	}
	for _, m := range as.Workspace.Mounts {
		ms := st.Mounts[m.Path]
		fmt.Fprintf(&b, "- `%s/`: %s.\n", m.Path, baseOrigin(ms.From))
	}
	b.WriteString("\n## Rules\n\n")
	b.WriteString("- Before working in a repository, read its own `AGENTS.md` (or `CLAUDE.md`) if it has one, and follow it inside that directory.\n")
	if as.Role == protocol.RoleAccept {
		b.WriteString("- You are reviewing the work in this task's workspace, as the implementation left it. Judge whether the task is done; do not fix it.\n")
		b.WriteString("- You may build and run tests, but do not edit the code, clean the working trees, or commit, switch branches or otherwise change git history: the next implementation continues from here.\n")
	} else {
		b.WriteString("- Make your changes in the working trees. Do not commit, switch or create branches, rebase, or add worktrees: tend records your changes when the run ends, and a changed history fails the run.\n")
	}
	b.WriteString("- Keep temporary copies and experiments out of the repositories; use the scratch directory you were given.\n")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("gitws: write AGENTS.md: %w", err)
	}
	return nil
}

// baseOrigin says where a base commit came from, given how ResolveBase
// found it.
func baseOrigin(from string) string {
	kind, branch, _ := strings.Cut(from, ":")
	switch {
	case kind == "commit":
		return "the commit the task pinned"
	case branch != "":
		return "the tip of `origin/" + branch + "` when the workspace was laid out; `origin/" + branch + "` may have moved since"
	}
	return "resolved by tend"
}
