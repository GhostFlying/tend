package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func checkWorkEnumError(t *testing.T, err error, field, value string, allowed any) {
	t.Helper()
	pe, ok := errors.AsType[*protocol.Error](err)
	if !ok || pe.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request for %s, got %v", field, err)
	}
	var details struct {
		Field         string   `json:"field"`
		Value         string   `json:"value"`
		AllowedValues []string `json:"allowed_values"`
	}
	if err := json.Unmarshal(pe.Details, &details); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(allowed)
	var want []string
	_ = json.Unmarshal(encoded, &want)
	if details.Field != field || details.Value != value || !reflect.DeepEqual(details.AllowedValues, want) {
		t.Fatalf("error details = %s, want field %s, value %q, allowed %s", pe.Details, field, value, encoded)
	}
	for _, text := range []string{field, value, string(encoded)} {
		if !strings.Contains(pe.Message, text) {
			t.Fatalf("message %q does not expose %q", pe.Message, text)
		}
	}
}

func TestWorkItemEnumValidationUsesSchemaVocabulary(t *testing.T) {
	base := func() protocol.IssuePlan {
		return protocol.IssuePlan{Title: "Plan", Project: "default", Intent: "Preserve the complete agreement"}
	}
	evidence := []protocol.IssueEvidence{{Kind: "owner_decision", Reference: "owner-source", Summary: "Owner agreement"}}
	for _, kind := range protocol.SharedFactKindValues() {
		plan := base()
		plan.Background = []protocol.SharedFact{{Kind: kind, Text: "Required context", Source: "owner-source"}}
		if err := ValidateIssue(plan, nil); err != nil {
			t.Fatalf("valid background %s: %v", kind, err)
		}
		plan.Background[0].Kind = "owner_request"
		checkWorkEnumError(t, ValidateIssue(plan, nil), "background[0].kind", "owner_request", protocol.SharedFactKindValues())
	}
	for _, status := range protocol.IssueStageStatusValues() {
		plan := base()
		plan.Stages = []protocol.IssueStage{{StageID: "design", Title: "Custom design phase", Status: status, Reason: "Owner revised scope", Evidence: evidence}}
		if err := ValidateIssue(plan, nil); err != nil {
			t.Fatalf("valid stage %s: %v", status, err)
		}
		plan.Stages[0].Status = "ready"
		checkWorkEnumError(t, ValidateIssue(plan, nil), "stages[0].status", "ready", protocol.IssueStageStatusValues())
	}
	for _, kind := range protocol.IssueFollowOnKindValues() {
		for _, status := range protocol.IssueFollowOnStatusValues() {
			plan := base()
			plan.FollowOns = []protocol.IssueFollowOn{{FollowOnID: "follow", Kind: kind, Status: status, Text: "Remaining work", Source: "owner-source", TransferredTo: "next-owner", Evidence: evidence}}
			if err := ValidateIssue(plan, nil); err != nil {
				t.Fatalf("valid follow-on %s/%s: %v", kind, status, err)
			}
			plan.FollowOns[0].Kind = "issue"
			checkWorkEnumError(t, ValidateIssue(plan, nil), "follow_ons[0].kind", "issue", protocol.IssueFollowOnKindValues())
			plan.FollowOns[0].Kind, plan.FollowOns[0].Status = kind, "done"
			checkWorkEnumError(t, ValidateIssue(plan, nil), "follow_ons[0].status", "done", protocol.IssueFollowOnStatusValues())
		}
	}
	for _, provider := range protocol.IssueDeliveryProviderValues() {
		plan := base()
		plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "review", Provider: provider, Repository: "example/project", Number: 1}}
		if err := ValidateIssue(plan, nil); err != nil {
			t.Fatalf("valid provider %s: %v", provider, err)
		}
		plan.Deliveries[0].Provider = "git"
		checkWorkEnumError(t, ValidateIssue(plan, nil), "deliveries[0].provider", "git", protocol.IssueDeliveryProviderValues())
	}
	for _, kind := range protocol.IssueEvidenceKindValues() {
		evidence := []protocol.IssueEvidence{{Kind: kind, Reference: "ref", Summary: "Verified fact"}}
		if err := ValidateIssueEvidence(evidence); err != nil {
			t.Fatalf("valid evidence %s: %v", kind, err)
		}
		evidence[0].Kind = "report"
		checkWorkEnumError(t, ValidateIssueEvidenceAt(evidence, "stages[1].evidence"), "stages[1].evidence[0].kind", "report", protocol.IssueEvidenceKindValues())
	}
	for _, kind := range protocol.IssueClosureKindValues() {
		closure := protocol.IssueClosure{Kind: kind, Reason: "Owner endpoint", Evidence: evidence}
		if err := ValidateIssueClosure(protocol.WorkItem{}, closure); err != nil {
			t.Fatalf("valid closure %s: %v", kind, err)
		}
		closure.Kind = "done"
		checkWorkEnumError(t, ValidateIssueClosure(protocol.WorkItem{}, closure), "closure.kind", "done", protocol.IssueClosureKindValues())
	}
	for _, status := range protocol.WorkItemUpdateStatusValues() {
		if err := ValidateIssueUpdateStatus(status); err != nil {
			t.Fatalf("valid update status %q: %v", status, err)
		}
	}
	checkWorkEnumError(t, ValidateIssueUpdateStatus("closed"), "status", "closed", protocol.WorkItemUpdateStatusValues())
	for _, action := range protocol.IssueTaskDispositionActionValues() {
		if err := ValidateIssueDispositionAction(action, "task_dispositions[1].action"); err != nil {
			t.Fatalf("valid disposition %s: %v", action, err)
		}
	}
	checkWorkEnumError(t, ValidateIssueDispositionAction("continue", "task_dispositions[1].action"), "task_dispositions[1].action", "continue", protocol.IssueTaskDispositionActionValues())
}
