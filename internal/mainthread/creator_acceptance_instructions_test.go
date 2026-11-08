package mainthread

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCreatorAcceptanceInstructionsReachNamedThreadConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   agentconfig.Purpose
		policy string
		want   []string
		absent []string
	}{
		{
			name: "top-level creator", role: agentconfig.Main,
			want:   []string{"set independent_acceptance.required with a reason", "Investigations and read-only reports may waive", "Missing policy and legacy contracts still require", "Existing tasks retain their frozen policies", "repository PR review, CI, merge and deployment obligations remain unchanged"},
			absent: []string{"retaining independent formal acceptance"},
		},
		{
			name: "Issue owner", role: agentconfig.IssueManager, policy: protocol.IssueOwnerPolicy,
			want:   []string{"decide its independent_acceptance.required", "investigations and read-only reports may waive", "Preserve genuinely explicit human independent-acceptance requirements", "pending reviews stay frozen", "exact current completed artifact_id as result evidence", "real passed acceptance_id", "Repository PR review, CI, merge and deployment obligations remain unchanged", "Task acceptance described below follows the same frozen policy", "Never waive actual provider, release-provenance, installation or controlled-upgrade evidence"},
			absent: []string{"and an explicit independent acceptance category", "Tend creates the bounded review child for a self-execution task", "preserve immutable execution evidence and independent acceptance"},
		},
		{
			name: "legacy manager", role: agentconfig.IssueManager,
			want:   []string{"missing policy and legacy contracts still require independent acceptance", "Current assignees and workers cannot self-exempt", "never execute native tools"},
			absent: []string{"workers with independent acceptance;"},
		},
		{
			name: "consultation", role: agentconfig.Consultation,
			want: []string{"Consultation cannot choose a waiver", "missing policy and legacy contracts still require independent acceptance"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{cfg: Config{Thread: ThreadConfig(t.TempDir(), "")}, named: &hostedconfig.Selection{Purpose: tc.role, OwnershipPolicy: tc.policy}}
			cfg := m.threadConfig()
			for _, want := range tc.want {
				if !strings.Contains(cfg.DeveloperInstructions, want) {
					t.Errorf("actual %s instructions omit %q", tc.name, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(cfg.DeveloperInstructions, absent) {
					t.Errorf("actual %s instructions still mandate %q", tc.name, absent)
				}
			}
			if cfg.Sandbox != "read-only" || cfg.Config["features.shell_tool"] != false || cfg.Config["features.multi_agent"] != false {
				t.Fatal("acceptance policy enabled native execution")
			}
		})
	}
	// The legacy task flow does not expose the creator-policy contract.
	if !strings.Contains(Instructions, "independent acceptance runs judge them") || !strings.Contains(Instructions, "using the returned acceptance_id as acceptance evidence, not the artifact_id") {
		t.Fatal("legacy acceptance semantics changed")
	}
}
