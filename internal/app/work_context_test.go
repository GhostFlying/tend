package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkContextProjectsRolesBeforeRunnerBoundary(t *testing.T) {
	spec := domain.Spec{Goal: "fix scrolling", Scope: "Browser behavior", ImplementationContext: "AUTHOR_DESIGN_USE_CACHE", SharedContext: []protocol.TaskWorkContext{{WorkItemID: "work", Revision: 3, Intent: "keep controls reachable", Constraints: []string{"Do not deploy"}, Facts: []protocol.SharedFact{{Kind: "observation", Text: "Controls overlap", Source: "owner screenshot"}}}}}
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		raw, err := workerSpec(spec, role)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Controls overlap", "owner screenshot", "Do not deploy", "revision 3"} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("%s lost %q: %s", role, want, raw)
			}
		}
		if strings.Contains(string(raw), "AUTHOR_DESIGN_USE_CACHE") != (role == protocol.RoleImplement) {
			t.Fatalf("role context leaked or was lost: %s %s", role, raw)
		}
		var projected domain.Spec
		if err := json.Unmarshal(raw, &projected); err != nil {
			t.Fatal(err)
		}
		if projected.ImplementationContext != "" || len(projected.SharedContext) != 0 {
			t.Fatal("unprojected fields crossed runner boundary")
		}
	}
	if spec.ImplementationContext != "AUTHOR_DESIGN_USE_CACHE" || spec.SharedContext[0].Revision != 3 {
		t.Fatal("projection changed frozen spec")
	}
}

func TestAcceptanceNarrativeIsNotInjectedUnlessItIsTheDeliverable(t *testing.T) {
	raw := `{"mounts":[{"commit_sha":"fixed"}],"files":[{"file_id":"original"}],"report":{"body":"AUTHOR_SELF_ASSESSMENT"}}`
	for _, required := range []bool{false, true} {
		spec := domain.Spec{Deliverables: []string{domain.DeliveryCommit}}
		if required {
			spec.Deliverables = append(spec.Deliverables, domain.DeliveryReport)
		}
		out, err := acceptanceArtifact(raw, spec)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "AUTHOR_SELF_ASSESSMENT") != required {
			t.Fatalf("report selection: %s", out)
		}
		if !strings.Contains(string(out), "fixed") || !strings.Contains(string(out), "original") {
			t.Fatalf("lost original evidence: %s", out)
		}
	}
}
