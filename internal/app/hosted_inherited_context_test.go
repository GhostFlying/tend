package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedManagerReceivesCurrentInheritedWorkerContext(t *testing.T) {
	h := hostedManagerHarness(t)
	createParent := func(intent, constraint string) protocol.Issue {
		return canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: intent, Project: "default", Intent: intent, Background: []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: constraint, Source: "owner brief"}}}}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
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
		var got struct {
			Inherited []protocol.TaskWorkContext `json:"inherited_context"`
		}
		if err = json.Unmarshal([]byte(text[strings.Index(text, "\n")+1:]), &got); err != nil {
			t.Fatal(err)
		}
		var want []protocol.TaskWorkContext
		if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			want, err = freezeCanonicalTaskContext(t.Context(), tx.Q(), child)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(got.Inherited) != 1 || !reflect.DeepEqual(got.Inherited, want[1:]) {
			t.Fatalf("manager inherited=%+v; worker inherited=%+v", got.Inherited, want[1:])
		}
		if !strings.Contains(text, ancestor.Plan.Intent) || !strings.Contains(text, ancestor.Plan.Background[0].Text) {
			t.Fatal("missing inherited goal/constraint", text)
		}
		if ancestor.IssueID == replacement.IssueID && strings.Contains(text, parent.Plan.Background[0].Text) {
			t.Fatal("stale ancestry", text)
		}
	}
}
