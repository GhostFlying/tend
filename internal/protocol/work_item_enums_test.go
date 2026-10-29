package protocol_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
)

func TestWorkItemEnumSchemaPreservesExistingVocabulary(t *testing.T) {
	g := schema.New()
	for _, values := range protocol.APIEnums() {
		g.Enum(values)
	}
	for _, root := range protocol.APITypes() {
		g.Add(root)
	}
	body, err := g.Document("WorkItemContract")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]struct {
			Enum       []string `json:"enum"`
			Required   []string `json:"required"`
			Properties map[string]struct {
				Ref string `json:"$ref"`
			} `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		owner, field, enum string
		values             []string
	}{
		{"SharedFact", "kind", "SharedFactKind", []string{"goal", "observation", "user_constraint", "decision", "input", "fact"}},
		{"WorkItemView", "status", "WorkItemStatus", []string{"open", "snoozed", "closed"}},
		{"UpdateWorkItemPayload", "status", "WorkItemUpdateStatus", []string{"", "open", "snoozed"}},
		{"IssueStage", "status", "IssueStageStatus", []string{"planned", "active", "done", "cancelled", "retired"}},
		{"IssueFollowOn", "kind", "IssueFollowOnKind", []string{"commitment", "problem", "suggestion"}},
		{"IssueFollowOn", "status", "IssueFollowOnStatus", []string{"open", "resolved", "transferred"}},
		{"IssueDelivery", "provider", "IssueDeliveryProvider", []string{"github", "codebase"}},
		{"IssueEvidence", "kind", "IssueEvidenceKind", []string{"acceptance", "delivery", "owner_decision", "upgrade"}},
		{"IssueClosure", "kind", "IssueClosureKind", []string{"completed", "declined", "handed_off", "superseded"}},
		{"IssueTaskDisposition", "action", "IssueTaskDispositionAction", []string{"continue_existing", "updated", "stopped"}},
	} {
		t.Run(tc.enum, func(t *testing.T) {
			if got := document.Definitions[tc.owner].Properties[tc.field].Ref; got != "#/definitions/"+tc.enum {
				t.Fatalf("%s.%s schema reference = %q", tc.owner, tc.field, got)
			}
			if got := document.Definitions[tc.enum].Enum; !reflect.DeepEqual(got, tc.values) {
				t.Fatalf("vocabulary = %v, want %v", got, tc.values)
			}
		})
	}
	if slices.Contains(document.Definitions["UpdateWorkItemPayload"].Required, "status") {
		t.Fatal("status became required instead of preserving omitted-as-unchanged")
	}
	if slices.Contains(document.Definitions["CreateWorkItemPayload"].Required, "background") {
		t.Fatal("background became required")
	}
}
