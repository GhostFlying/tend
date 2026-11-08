package mainthread

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedIssueOwnerPlansOnePRReviewAndAssessesExistingChecks(t *testing.T) {
	m := &Manager{cfg: Config{Thread: ThreadConfig(t.TempDir(), "")}, named: &hostedconfig.Selection{Purpose: agentconfig.IssueManager, OwnershipPolicy: protocol.IssueOwnerPolicy}}
	cfg := m.threadConfig()
	for _, guidance := range []string{"draft PR", "unique whole-PR reviewer", "PR preparation and intermediate reports", "owner self-check", "explicit human", "frozen", "candidate-matching implementation reports", "reuse", "targeted", "full", "explain reruns", "not a new approval", "repository"} {
		if !strings.Contains(cfg.DeveloperInstructions, guidance) {
			t.Errorf("actual Issue-owner configuration lacks %q", guidance)
		}
	}
	if cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never" || cfg.Config["features.shell_tool"] != false {
		t.Fatal("review efficiency changed the execution adapter's permissions")
	}
}

func TestNamedOwnerIncrementalReadingGuidancePreservesScopeAndSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   agentconfig.Purpose
		policy string
		owner  bool
	}{
		{"Issue owner", agentconfig.IssueManager, protocol.IssueOwnerPolicy, true},
		{"global planner", agentconfig.Main, "", false},
		{"legacy manager", agentconfig.IssueManager, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{cfg: Config{Thread: ThreadConfig(t.TempDir(), "")}, named: &hostedconfig.Selection{Purpose: tc.role, OwnershipPolicy: tc.policy}}
			instructions := m.threadConfig().DeveloperInstructions
			if strings.Contains(instructions, "issues_context_read view=incremental by default") != tc.owner {
				t.Fatal("incremental owner guidance changed another configured role", instructions)
			}
			if !tc.owner {
				return
			}
			for _, guidance := range []string{"omit cursor for a snapshot", "next_cursor", "more=true", "empty items page", "content_id", "through bounds event membership", "newer event remains", "fresh snapshot without cursor", "after_source only for legacy", "not source review", "without a mandatory full conversation scan", "Continue the existing legal Session binding"} {
				if !strings.Contains(instructions, guidance) {
					t.Errorf("owner read guidance lost %q", guidance)
				}
			}
		})
	}
}
