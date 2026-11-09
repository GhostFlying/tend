package gitws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkspaceGuidesAllowPrivateRuntimeTemporaryDirectories(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		root := t.TempDir()
		as := protocol.Assignment{Role: role}
		if err := writeAgentsFile(root, as, state{}); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, guide := range []string{string(body), repoGuide(as, state{})} {
			for _, want := range []string{"evidence for submission", "scratch directory", "private OS temporary directories", "short real TMPDIR", "runtime temporary files need not stay in scratch", "required file submission"} {
				if !strings.Contains(guide, want) {
					t.Errorf("%s guide lacks %q: %s", role, want, guide)
				}
			}
			if strings.Contains(guide, "Keep temporary copies and experiments out of the repositories; use the scratch directory") {
				t.Errorf("%s guide still applies the old blanket temporary-file restriction", role)
			}
			if role == protocol.RoleAccept && !strings.Contains(guide, "do not edit the code") {
				t.Error("runtime files must not relax the original review restrictions")
			}
		}
	}
}
