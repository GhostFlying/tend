package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBrief(t *testing.T) {
	spec := WorkerSpec{Role: protocol.RoleImplement, Dir: "/w/feature_a", ScratchDir: "/s", Attempt: 2,
		Spec:     json.RawMessage(`{"goal":"Add Sub","constraints":["no new deps"],"acceptance":["go test passes"]}`),
		Feedback: &protocol.Feedback{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["handle overflow"]`)}}
	instructions, input := Brief(spec)
	for _, want := range []string{"Add Sub", "no new deps", "go test passes", "handle overflow", "interrupted"} {
		if !strings.Contains(input, want) {
			t.Errorf("implement input lacks %q:\n%s", want, input)
		}
	}
	if !strings.Contains(instructions, "Whether and how to commit is yours to decide") || !strings.Contains(instructions, "/w/feature_a") {
		t.Errorf("implement instructions:\n%s", instructions)
	}
	spec.Role, spec.Feedback, spec.Attempt = protocol.RoleAccept, nil, 1
	instructions, input = Brief(spec)
	if !strings.Contains(instructions, VerdictTool) || !strings.Contains(instructions, "blocked") || !strings.Contains(input, "Review") {
		t.Errorf("acceptance brief:\n%s\n%s", instructions, input)
	}
	if !json.Valid(VerdictToolSpec.InputSchema) {
		t.Error("verdict tool schema is not JSON")
	}
}

func TestBriefCarriesTaskRelationsWithoutWorkspaceInheritance(t *testing.T) {
	_, input := Brief(WorkerSpec{Relations: []protocol.TaskRelationView{{Kind: protocol.RelationFollowUp, Direction: protocol.RelationOutgoing, Task: protocol.TaskSummary{TaskID: "task_original", Title: "original delivery", SpecVersion: 3, Lifecycle: protocol.TaskCompleted}}}})
	for _, want := range []string{"follow_up outgoing", "task_original", "spec 3", "completed", "configured workspace base"} {
		if !strings.Contains(input, want) {
			t.Fatalf("brief lacks %q: %s", want, input)
		}
	}
}
