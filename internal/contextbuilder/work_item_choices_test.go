package contextbuilder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWorkItemChoiceEventsReachConfirmedHostedDelta(t *testing.T) {
	for _, action := range []string{"respond", "withdraw", "replace"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			var item protocol.WorkItem
			raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: protocol.WorkItemPlan{Title: "Track delivery", Intent: "Review next steps", Project: "default"}}, app.CreateIssue{})
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			source, _ := e.message("owner", "Implement, then discuss deployment", "")
			if err := e.b.Confirm(t.Context(), e.prepare(0).ID); err != nil {
				t.Fatal(err)
			}
			check := func(want ...string) {
				t.Helper()
				var composed Composed
				watermark := e.baseline().Watermark
				if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					composed, err = e.b.Compose(t.Context(), tx.Q(), ComposeRequest{Self: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "thread-main"}, Watermark: watermark})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				for _, text := range want {
					if !strings.Contains(composed.Text, text) {
						t.Fatalf("hosted delta lacks %q: %s", text, composed.Text)
					}
				}
				p := e.prepare(0)
				if p == nil || p.Full {
					t.Fatalf("expected delta after confirmed baseline: %+v", p)
				}
				if err := e.b.Confirm(t.Context(), p.ID); err != nil {
					t.Fatal(err)
				}
			}
			var choice protocol.WorkItemChoice
			raw = e.exec(owner, "work_item_choices.create", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &item.Revision, protocol.CreateWorkItemChoicePayload{Question: "Deploy now?", Options: []string{"Continue", "Later"}, SourceID: source}, app.CreateWorkItemChoice{})
			if err := json.Unmarshal(raw, &choice); err != nil {
				t.Fatal(err)
			}
			check("work_item_choice.opened", choice.ChoiceID, "Deploy now?", "v1 open")
			payload := protocol.UpdateWorkItemChoicePayload{Answer: "Later, after manual inspection"}
			expected := []string{"work_item_choice.answered", "v2 answered", payload.Answer}
			if action == "withdraw" {
				expected = []string{"work_item_choice.withdrawn", "v2 withdrawn", "Already deployed"}
			}
			if action == "replace" {
				payload.Replacement = &protocol.CreateWorkItemChoicePayload{Question: "Merged already; deploy now?", Options: []string{"Deploy", "Later"}, SourceID: source}
				expected = []string{"work_item_choice.superseded", "v2 superseded", "replaced by", "work_item_choice.opened", "Merged already; deploy now?"}
			}
			bytes, _ := json.Marshal(payload)
			result, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "work_item_choices." + action, Target: protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}, ExpectedControlRevision: &choice.Version, Reason: "Already deployed", Payload: bytes}, app.UpdateWorkItemChoice{Action: action})
			if err != nil || result.Status != protocol.OpSucceeded {
				t.Fatalf("mutate choice: %+v %v", result, err)
			}
			check(expected...)
			// Closing is a scoped plan change. Other projects only see actionable choices.
			var scoped Snapshot
			if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				latest, err := store.LatestEventSeq(t.Context(), tx.Q())
				if err != nil {
					return err
				}
				return e.b.gatherDelta(t.Context(), tx.Q(), &scoped, 0, latest, protocol.Principal{}, &scope{project: "other", labels: true})
			}); err != nil {
				t.Fatal(err)
			}
			if len(scoped.Changes) != 0 || len(scoped.Attention) != 0 {
				t.Fatalf("foreign choice leaked to own project: %+v", scoped)
			}
			if !strings.Contains(strings.Join(scoped.OtherAttention, "\n"), "[default]") {
				t.Fatalf("actionable foreign choice lost its project: %+v", scoped)
			}
		})
	}
}

func TestWorkItemChoiceSummaryLabelsAddonRelay(t *testing.T) {
	c := protocol.WorkItemChoice{ChoiceID: "choice-example", Version: 2, Status: "answered", Question: "Deploy?", Answer: "Later", AnswerSourceID: "message-example", AnswerSourceKind: "addon_relay"}
	line := workItemChoiceLine(c)
	if !strings.Contains(line, "owner answer relayed by addon agent") || !strings.Contains(line, "Later") || !strings.Contains(line, "message-example") {
		t.Fatal(line)
	}
	c.AnswerSourceKind = "owner_message"
	if line := workItemChoiceLine(c); strings.Contains(line, "relayed") || !strings.Contains(line, "owner answer:") {
		t.Fatal(line)
	}
}
