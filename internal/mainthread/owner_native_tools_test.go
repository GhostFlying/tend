package mainthread

import (
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueOwnerNativeToolsRequireNegotiatedRunnerMode(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection hostedconfig.Selection
		native    bool
	}{
		{name: "negotiated_owner", selection: hostedconfig.Selection{RunnerManaged: true, NativeOwnerTools: true, Purpose: agentconfig.IssueManager, OwnershipPolicy: protocol.IssueOwnerPolicy}, native: true},
		{name: "older_runner", selection: hostedconfig.Selection{RunnerManaged: true, Purpose: agentconfig.IssueManager, OwnershipPolicy: protocol.IssueOwnerPolicy}},
		{name: "receipt_reader", selection: hostedconfig.Selection{RunnerManaged: true, NativeOwnerTools: true, ReceiptOnly: true, Purpose: agentconfig.IssueManager, OwnershipPolicy: protocol.IssueOwnerPolicy}},
		{name: "global_planner", selection: hostedconfig.Selection{NativeOwnerTools: true, Purpose: agentconfig.Main}, native: true},
		{name: "consultation", selection: hostedconfig.Selection{RunnerManaged: true, NativeOwnerTools: true, Purpose: agentconfig.Consultation}, native: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.selection.Definition.Model, tc.selection.Definition.Effort = "fixed-model", "high"
			m := &Manager{cfg: Config{Thread: ThreadConfig(t.TempDir(), "")}, named: &tc.selection}
			cfg := m.threadConfig()
			if (cfg.Purpose == coordination.NativeOwnerPurpose) != tc.native {
				t.Fatalf("native mode negotiation differs: %+v", cfg)
			}
			if cfg.Model != "fixed-model" || cfg.Config["model_reasoning_effort"] != "high" {
				t.Fatal("configured execution changed", cfg)
			}
			restricted := tc.selection.ReceiptOnly || tc.selection.RequireReceiptReader || tc.selection.RunnerManaged && !tc.selection.NativeOwnerTools
			if restricted {
				if cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never" {
					t.Fatal("receipt/transport-only mode lost restriction", cfg)
				}
			} else if cfg.Sandbox != "" || cfg.ApprovalPolicy != "" {
				t.Fatal("responsibility changed runtime policy", cfg)
			}
		})
	}
}
