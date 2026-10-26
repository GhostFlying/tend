package domain

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestObservationScopeValidationAndEquality(t *testing.T) {
	base := Spec{Goal: "inspect", Acceptance: []string{"verified"}, Deliverables: []string{DeliveryReport}, Workspace: Workspace{Mode: "temporary"}}
	for _, scope := range []*protocol.ObservationScope{{}, {WorkItemIDs: []string{"bad"}}, {WorkItemIDs: []string{"wi_01ARZ3NDEKTSV4RRFFQ69G5FAV", "wi_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}} {
		spec := base
		spec.Observation = scope
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid scope allowed: %+v", scope)
		}
	}
	a := base
	a.Observation = &protocol.ObservationScope{Deployment: true}
	if a.Equal(base) || base.Equal(a) {
		t.Fatal("observation grant ignored")
	}
	b := base
	b.Observation = &protocol.ObservationScope{Deployment: true}
	if !a.Equal(b) {
		t.Fatal("equal scope differs")
	}
	b.Observation.WorkItemIDs = []string{"wi_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	if a.Equal(b) {
		t.Fatal("work item grant ignored")
	}
}
