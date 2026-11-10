package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHostedManagerReceivesCurrentInheritedWorkerContext(t *testing.T) {
	h := hostedManagerHarness(t)
	createParent := func(intent, constraint string) protocol.Issue {
		return admitExistingIssueFixture(t, h, canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: intent, Project: "default", Intent: intent, Background: []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: constraint, Source: "owner brief"}}}}, nil), CanonicalOwnerIssueCommand{Action: "create"})))
	}
	parent := createParent("Preserve the stable service", "Do not change the public API")
	replacement := createParent("Investigate a separate local fixture", "Never deploy this fixture")
	child := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	for _, ancestor := range []protocol.Issue{parent, replacement} {
		p := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch}, ParentIssueID: ancestor.IssueID, ExpectedParentRevision: rev(ancestor.Revision)}
		child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", p))
		text, err := h.app.AgentContext(t.Context(), "issue:"+child.IssueID, h.ids.New(protocol.PrefixRequest))
		if err != nil {
			t.Fatal(err)
		}
		var got protocol.ConversationContextIndex
		if err = json.Unmarshal([]byte(text[strings.Index(text, "{\"format\""):]), &got); err != nil {
			t.Fatal(err)
		}
		if got.IssueID != child.IssueID || len(text) > protocol.ContextIndexBudget {
			t.Fatal("missing bounded scoped index", text)
		}
		if strings.Contains(text, ancestor.Plan.Intent) || strings.Contains(text, ancestor.Plan.Background[0].Text) {
			t.Fatal("injected inherited prose", text)
		}
		// Originals remain in the current ancestry material view; an index
		// neither erases them nor grants control over the ancestor.
		materials, err := h.app.ReadScopedIssueMaterials(t.Context(), child.IssueID, child.IssueID, protocol.IssueMaterialsQuery{Kind: "issues", ID: ancestor.IssueID})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(materials)
		if !strings.Contains(string(raw), ancestor.Plan.Background[0].Text) {
			t.Fatal("inherited original unavailable", string(raw))
		}
		if ancestor.IssueID == replacement.IssueID && strings.Contains(string(raw), parent.Plan.Background[0].Text) {
			t.Fatal("stale ancestry material")
		}

	}
}
