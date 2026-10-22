package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestDeliverablesPreserveLegacySpecEncodingAndSemanticEquality(t *testing.T) {
	legacy := Spec{Goal: "goal", Acceptance: []string{"checked"}, Delivery: DeliveryCommit, Workspace: Workspace{Mounts: []Mount{{Repo: "repo"}}}}
	body, err := json.Marshal(legacy.Normalize())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "deliverables") {
		t.Fatalf("legacy JSON acquired new field: %s", body)
	}
	explicit := legacy
	explicit.Delivery = ""
	explicit.Deliverables = []string{DeliveryCommit}
	if !legacy.Equal(explicit) {
		t.Fatal("equivalent legacy and composable commit specs differ")
	}
	if err := explicit.Validate(); err != nil {
		t.Fatal(err)
	}
	left := legacy
	left.Deliverables = []string{DeliveryReport, DeliveryCommit, DeliveryReport}
	right := legacy
	right.Deliverables = []string{DeliveryCommit, DeliveryReport}
	if !left.Equal(right) {
		t.Fatal("deliverable ordering changes spec semantics")
	}
	if !slices.Equal(left.RequiredDeliverables(), []string{DeliveryCommit, DeliveryReport}) {
		t.Fatalf("required=%v", left.RequiredDeliverables())
	}
}

func TestDeliverablesRejectEmptyAndUnknownOutputs(t *testing.T) {
	spec := Spec{Workspace: Workspace{Mounts: []Mount{{Repo: "repo"}}}}
	if err := spec.Validate(); err == nil {
		t.Fatal("empty delivery accepted")
	}
	spec.Deliverables = []string{"unknown"}
	if err := spec.Validate(); err == nil {
		t.Fatal("unknown delivery accepted")
	}
}
