package mainthread

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
)

func TestIssueReuseInstructionsReachActualNamedMain(t *testing.T) {
	m := &Manager{named: &hostedconfig.Selection{Purpose: agentconfig.Main}}
	cfg := m.threadConfig()
	for _, text := range []string{"issues_reuse_status", "issues_reuse", "confirmed original creation request", "content/association revisions", "existing review duties", "Create a distinct new responsibility", "reconcile that original request ID", "does not technically plan tasks"} {
		if !strings.Contains(cfg.DeveloperInstructions, text) {
			t.Fatal("Main lacks explicit responsibility reuse contract", text)
		}
	}
	for _, name := range []string{"issues_reuse_status", "issues_reuse"} {
		found := false
		for _, tool := range cfg.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatal("instruction refers to absent Main tool", name)
		}
	}
}
