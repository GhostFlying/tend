package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol/schema"
)

func TestPublicationAndDeliveryInputContracts(t *testing.T) {
	for _, purpose := range []string{"", "progress", "final"} {
		if !ValidPublicationPurpose(purpose) {
			t.Fatal(purpose)
		}
	}
	if ValidPublicationPurpose("complete") {
		t.Fatal("business completion marker accepted")
	}
	for _, tc := range []struct {
		p     UpdateIssueDeliveryPayload
		field string
	}{
		{UpdateIssueDeliveryPayload{Action: "register_pr"}, "pr_url"},
		{UpdateIssueDeliveryPayload{Action: "register_pr", PRURL: "https://example.invalid/a/b/pull/1"}, "GitHub.com"},
		{UpdateIssueDeliveryPayload{Action: "report", TaskID: "original", PRURL: "https://github.com/a/b/pull/1"}, "register_pr"},
		{UpdateIssueDeliveryPayload{Action: "recover_preparation", TaskID: "original"}, "expected_task_control_revision"},
		{UpdateIssueDeliveryPayload{Action: "bind", TaskID: "original", ExecutionTaskID: "same"}, "expected_task_control_revision"},
		{UpdateIssueDeliveryPayload{Action: "complete", TaskID: "original", ExecutionRunID: "old"}, "execution_task_id"},
	} {
		tc.p.Reason = "Preserve actual responsibility"
		err := tc.p.ValidateInput()
		if CodeOf(err) != CodeInvalidRequest || !strings.Contains(err.Error(), tc.field) {
			t.Fatal(tc, err)
		}
	}
	for _, p := range []UpdateIssueDeliveryPayload{{Action: "register_pr", PRURL: "https://github.com/a/b/pull/1"}, {Action: "submit_result", Result: &ArtifactPayload{}}, {Action: "report", TaskID: "source"}, {Action: "resume", TaskID: "source"}, {Action: "prepare", TaskID: "source"}, {Action: "report", TaskID: "source", UpgradeID: "historically-ignored"}, {Action: "submit_result", Result: &ArtifactPayload{}, Candidate: &IssueDeliveryCandidate{}}, {Action: "complete", TaskID: "source", ExecutionTaskID: "history", ExecutionRunID: "original", ExecutionDutyRevision: 2}} {
		p.Reason = "Actual result"
		if err := p.ValidateInput(); err != nil {
			t.Fatal(p, err)
		}
	}
	g := schema.New()
	g.Add(MessagingPublication{})
	g.Add(CoordinationReply{})
	g.Add(UpdateIssueDeliveryPayload{})
	raw, err := g.Document("input")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions map[string]map[string]any `json:"definitions"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("invalid schema")
	}
	for _, name := range []string{"MessagingPublication", "CoordinationReply"} {
		d := doc.Definitions[name]
		for _, r := range d["required"].([]any) {
			if r == "purpose" || r == "body" {
				t.Fatal("ordinary file-only publication remains gated", d)
			}
		}
		if d["allOf"] == nil {
			t.Fatal("optional marker/body contract lost", d)
		}
	}
	branches := doc.Definitions["UpdateIssueDeliveryPayload"]["anyOf"].([]any)
	if len(branches) != 9 {
		t.Fatal("actions missing", branches)
	}
}
