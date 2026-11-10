package mainthread

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueOwnerInstructionsPreserveAutonomyAndDurableContinuity(t *testing.T) {
	m := &Manager{cfg: Config{Thread: ThreadConfig(t.TempDir(), "")}, named: &hostedconfig.Selection{Purpose: agentconfig.IssueManager, OwnershipPolicy: protocol.IssueOwnerPolicy}}
	text := m.threadConfig().DeveloperInstructions
	for _, want := range []string{"investigate, implement and self-test directly", "Persistent records are the business context source", "Session reuse is a token optimization", "tasks_continue", "one sustained review Task on the original Issue", "runs_wait_external", "physical stop", "conversation_notify", "pr_policy.deployment", "never invent a worker Run", "unknown external operation"} {
		if !strings.Contains(text, want) {
			t.Errorf("actual owner contract lost %q", want)
		}
	}
	for _, obsolete := range []string{"commit the turn", "Never reopen a completed task", "Waiting retains the run and execution slot", "For manual integration or any deployment", "Read the current conversation_turn_read state and commit"} {
		if strings.Contains(text, obsolete) {
			t.Errorf("actual owner contract retains obsolete rule %q", obsolete)
		}
	}
}

func TestNamedCommunicationDoesNotRequireSourceReviewToPublishFinal(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		m := &Manager{named: &hostedconfig.Selection{Purpose: purpose}}
		text := m.threadConfig().DeveloperInstructions
		if !strings.Contains(text, "obsolete input") || !strings.Contains(text, "actual attachment references") {
			t.Fatalf("%s lacks actual final-publication contract", purpose)
		}
		for _, obsolete := range []string{"Supply all seven reviewed_kinds", "then conversation_turn_commit", "Review your exact admitted source/context versions and commit"} {
			if strings.Contains(text, obsolete) {
				t.Errorf("%s retains publication ceremony %q", purpose, obsolete)
			}
		}
	}
}

func TestNamedResponsibilitiesPreserveTheSelectedRuntimeCapabilities(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		for _, policy := range []string{"", protocol.IssueOwnerPolicy} {
			base := ThreadConfig(t.TempDir(), "selected-model")
			base.Sandbox, base.ApprovalPolicy = "workspace-write", "never"
			base.Config = map[string]any{"features.shell_tool": true, "features.apply_patch_freeform": true, "features.multi_agent": true, "features.image_generation": false}
			m := &Manager{cfg: Config{Thread: base}, named: &hostedconfig.Selection{Purpose: purpose, OwnershipPolicy: policy, Definition: agentconfig.AgentDefinition{Model: "selected-model", Effort: "high"}}}
			cfg := m.threadConfig()
			if cfg.Sandbox != base.Sandbox || cfg.ApprovalPolicy != base.ApprovalPolicy || cfg.Config["features.shell_tool"] != true || cfg.Config["features.apply_patch_freeform"] != true || cfg.Config["features.multi_agent"] != true || cfg.Config["features.image_generation"] != false {
				t.Fatal("responsibility changed actual configured capability policy", purpose, policy, cfg)
			}
			for _, ban := range []string{"Never create or technically plan tasks, investigate", "without native execution", "never execute native tools", "native command/code/research/delegation tools remain unavailable"} {
				if strings.Contains(cfg.DeveloperInstructions, ban) {
					t.Fatal("role prompt denied configured tools", purpose, ban)
				}
			}
		}
	}
}

func TestFallbackInstructionsUseActualCapabilitiesAndSameGoalContinuation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		selection *hostedconfig.Selection
	}{
		{"default-main", nil},
		{"named-main", &hostedconfig.Selection{Purpose: agentconfig.Main}},
		{"named-consultation", &hostedconfig.Selection{Purpose: agentconfig.Consultation}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := ThreadConfig(t.TempDir(), "configured-model")
			base.Config = map[string]any{"features.shell_tool": true, "features.apply_patch_freeform": true, "features.image_generation": false}
			cfg := (&Manager{cfg: Config{Thread: base}, named: tt.selection}).threadConfig()
			if cfg.Config["features.shell_tool"] != true || cfg.Config["features.apply_patch_freeform"] != true || cfg.Config["features.image_generation"] != false {
				t.Fatal("fallback changed explicit runtime capability configuration", cfg)
			}
			for _, obsolete := range []string{"Technical diagnosis belongs to the worker", "Use only the person-facing planning tools", "Do not reopen a completed Task; use follow_up", "which the server excludes from independent acceptance", "Do not attach implementation-only design material to an independent reviewer"} {
				if strings.Contains(cfg.DeveloperInstructions, obsolete) {
					t.Fatalf("fallback retained role-based capability/continuation prohibition %q", obsolete)
				}
			}
			if !strings.Contains(cfg.DeveloperInstructions, "category/capabilities") || !(strings.Contains(cfg.DeveloperInstructions, "runtime tools") || strings.Contains(cfg.DeveloperInstructions, "native tools")) || !strings.Contains(cfg.DeveloperInstructions, "authority") {
				t.Fatal("fallback lost configured capability and real authority boundaries", cfg.DeveloperInstructions)
			}
			if tt.selection == nil && !strings.Contains(cfg.DeveloperInstructions, "tasks_continue") {
				t.Fatal("fallback split same-goal completed Tasks")
			}
		})
	}
}
