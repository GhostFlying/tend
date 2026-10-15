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
	b.WriteString("| Directory | Repository | Branch | Starts at |\n| --- | --- | --- | --- |\n")
	for _, m := range as.Workspace.Mounts {
		ms := st.Mounts[m.Path]
		branch := ms.Branch
		if branch == "" {
			branch = "(detached)"
		}
		fmt.Fprintf(&b, "| `%s/` | %s | `%s` | `%s` (%s) |\n", m.Path, m.Repo, branch, short(ms.Base), ms.From)
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

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
