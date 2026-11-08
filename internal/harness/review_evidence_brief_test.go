package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestReviewerBriefAssessesOriginalEvidenceBeforeChoosingChecks(t *testing.T) {
	artifact, err := json.Marshal(protocol.ArtifactPayload{Report: &protocol.ResultReport{
		Body:     "Implementation verified candidate head aabb, tree ccdd, base eeff; go test ./changed passed on Linux; full check not provided",
		Evidence: []string{"original-check-receipt"}, Uncertainties: []string{"browser coverage not provided"},
	}, Files: []protocol.ArtifactFile{{FileID: "file_original_log", Name: "checks.log", Size: 43, SHA256: strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, hasContext := range []bool{false, true} {
		spec := WorkerSpec{Role: protocol.RoleAccept, Spec: json.RawMessage(`{"acceptance":["candidate meets repository rules"]}`), Artifact: artifact}
		if hasContext {
			spec.ReadContext = func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
				return protocol.RunContext{}, nil
			}
		}
		instructions, input := Brief(spec)
		for _, original := range []string{"head aabb, tree ccdd, base eeff", "go test ./changed passed", "full check not provided", "original-check-receipt", "file_original_log", "browser coverage not provided"} {
			if !strings.Contains(input, original) {
				t.Errorf("acceptance lost original evidence %q", original)
			}
		}
		for _, guidance := range []string{"Before choosing checks, read", "candidate-matching implementation report", "trustworthy", "reuse", "targeted", "full", "Missing fields", "why you rerun", "does not require approval", "do not edit code", "submit_verdict"} {
			if !strings.Contains(instructions, guidance) {
				t.Errorf("reviewer lacks evidence-selection guidance %q: %s", guidance, instructions)
			}
		}
		if strings.Contains(instructions, "Use read_context kind=reviews") != hasContext {
			t.Fatal("review guidance advertises an unavailable reader or hides the available one", instructions)
		}
	}
}

func TestReportReviewerBriefCanDiscoverContributorEvidenceWithoutMandatoryAcceptance(t *testing.T) {
	spec := WorkerSpec{Role: protocol.RoleImplement, Spec: json.RawMessage(`{"goal":"Review the whole PR and report findings","deliverables":["report"]}`), ReadContext: func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
		return protocol.RunContext{}, nil
	}}
	instructions, input := Brief(spec)
	for _, guidance := range []string{"whole-PR reviewer", "read_context kind=reviews", "bounded index", "linked contributor evidence", "original artifacts", "not the complete report", "reuse", "targeted", "why you rerun", "frozen acceptance policy", "explicit human", "submit_result"} {
		if !strings.Contains(instructions, guidance) {
			t.Errorf("report reviewer lacks %q: %s", guidance, instructions)
		}
	}
	if strings.Contains(instructions, "An independent reviewer will check your work") || !strings.Contains(input, "Review the whole PR and report findings") {
		t.Fatal("a report was turned into a mandatory second review", instructions, input)
	}
}
