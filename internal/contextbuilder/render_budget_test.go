package contextbuilder

import (
	"fmt"
	"strings"
	"testing"
)

func TestAddonInstructionsDoNotDisplaceCompletionAndDecisions(t *testing.T) {
	s := Snapshot{
		PacketID: "pkt_1", Epoch: 1, Full: true, Addon: true,
		Decisions:       []string{"dec_1 v1: 选择下一步？Options: continue / hold"},
		Unseen:          []string{`task_1 "preview" task.completed (by tend)`},
		UnseenAttention: []string{"opened: dec_1 v1 about task_1"},
		Tasks:           []string{`task_2 "next task": queued`},
	}
	budget := 1500 // Less than the fixed instructions, enough for these facts.
	out := Render(s, budget)
	for _, want := range []string{AddonInstructions, s.Decisions[0], s.Unseen[0], s.UnseenAttention[0], s.Tasks[0]} {
		if !strings.Contains(out, want) {
			t.Fatalf("fixed instructions displaced a ledger fact: missing %q", want)
		}
	}
	if strings.Contains(out, "more;") || len(out)-len(AddonInstructions) > budget {
		t.Fatalf("facts should fit their own budget: %s", out)
	}
}

func TestAddonDynamicBudgetStillCompactsLargeLedgers(t *testing.T) {
	s := Snapshot{
		PacketID: "pkt_1", Full: true, Addon: true,
		Decisions: []string{"dec_1 v1: Ready to continue?"},
		Unseen:    []string{"task_complete task.completed (by tend)"},
	}
	for i := range 100 {
		s.Tasks = append(s.Tasks, fmt.Sprintf("task_%03d: queued; 原始文件保持不变", i))
	}
	budget := (Config{}).withDefaults().HookFullBudget
	out := Render(s, budget)
	for _, want := range []string{AddonInstructions, s.Decisions[0], s.Unseen[0], "more open tasks; see tasks_list"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q from compacted ledger", want)
		}
	}
	// Section headings and omission notes have a small conservative estimate.
	if dynamicBytes := len(out) - len(AddonInstructions); dynamicBytes > budget+150 {
		t.Fatalf("dynamic ledger escaped its byte budget: %d > %d", dynamicBytes, budget)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- task_0") && !strings.HasSuffix(line, "原始文件保持不变") {
			t.Fatalf("partially emitted task: %q", line)
		}
	}
}
