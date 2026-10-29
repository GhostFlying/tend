package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWorkItemChoiceOwnerAPIReadAnswerAndStaleVersion(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	e.configure()
	token := e.login(protocol.ScopeOwner)
	agent := e.retiredAgentToken("choice-agent")
	var item protocol.WorkItem
	if err := json.Unmarshal(e.exec("work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Implement page", Intent: "Improve the page"}}, app.CreateIssue{}), &item); err != nil {
		t.Fatal(err)
	}
	submitted := e.envelope("conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "Implement the page, then ask about deployment"})
	if status, response := e.op(token, submitted); status != http.StatusOK || response.Operation == nil || (response.Operation.Status != protocol.OpSucceeded && response.Operation.Status != protocol.OpAccepted) {
		t.Fatalf("source message: %d %+v", status, response)
	}
	_, page := e.conversation(token, "?limit=10")
	if len(page.Items) != 1 {
		t.Fatalf("source conversation: %+v", page)
	}
	var choice protocol.WorkItemChoice
	payload := protocol.CreateWorkItemChoicePayload{Question: "Continue to deployment?", Options: []string{"Continue", "Later"}, SourceID: page.Items[0].MessageID}
	if err := json.Unmarshal(e.exec("work_item_choices.create", protocol.Target{Type: "work_item", ID: item.WorkItemID}, &item.Revision, payload, app.CreateWorkItemChoice{}), &choice); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/work-item-choices/" + choice.ChoiceID
	if status := e.do("GET", path, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("no auth: %d", status)
	}
	if status := e.do("GET", path, agent, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("agent read: %d", status)
	}
	if status := e.do("GET", "/api/v1/work-item-choices/missing", token, nil, nil); status != http.StatusNotFound {
		t.Fatalf("missing: %d", status)
	}
	var read protocol.WorkItemChoice
	if status := e.do("GET", path, token, nil, &read); status != http.StatusOK || read.ChoiceID != choice.ChoiceID || read.Status != "open" || read.Version != 1 {
		t.Fatalf("read open: %d %+v", status, read)
	}
	target := protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}
	version := int64(1)
	answer := e.envelope("work_item_choices.respond", target, &version, protocol.UpdateWorkItemChoicePayload{Answer: "Later, after I inspect the result"})
	if status, _ := e.op(agent, answer); status != http.StatusUnauthorized {
		t.Fatalf("agent answer: %d", status)
	}
	status, response := e.op(token, answer)
	if status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("answer: %d %+v", status, response)
	}
	if status := e.do("GET", path, token, nil, &read); status != http.StatusOK || read.Status != "answered" || read.Version != 2 || read.Answer != "Later, after I inspect the result" || read.AnswerSourceID == "" {
		t.Fatalf("read answer: %d %+v", status, read)
	}
	// Replaying one response must not append another owner answer or change version.
	if status, response := e.op(token, answer); status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("replay: %d %+v", status, response)
	}
	stale := e.envelope("work_item_choices.respond", target, &version, protocol.UpdateWorkItemChoicePayload{Answer: "Continue"})
	_, rejected := e.op(token, stale)
	if rejected.Operation == nil || rejected.Operation.Error == nil || rejected.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale: %+v", rejected)
	}
	if status := e.do("GET", path, token, nil, &read); status != http.StatusOK || read.Version != 2 || read.Answer != "Later, after I inspect the result" {
		t.Fatalf("stale changed answer: %d %+v", status, read)
	}
	// The question's original read/respond surface remains current; its old
	// WorkItem backlink is only historical ledger data, not an active facade.
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		choices, err := store.ListWorkItemChoices(t.Context(), tx.Q(), item.WorkItemID, "")
		if err == nil && (len(choices) != 1 || choices[0].Status != "answered") {
			t.Fatalf("historical choice fact: %+v", choices)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, page = e.conversation(token, "?limit=50")
	answeredMessages, projectedCards := 0, 0
	for _, message := range page.Items {
		if message.MessageID == read.AnswerSourceID {
			answeredMessages++
			if message.Author != "owner" || message.Body != read.Answer {
				t.Fatalf("answer provenance: %+v", message)
			}
		}
		for _, card := range message.Choices {
			if card.ChoiceID == choice.ChoiceID {
				projectedCards++
				if card.Status != "answered" || card.Version != 2 {
					t.Fatalf("stale chat card: %+v", card)
				}
			}
		}
	}
	if answeredMessages != 1 || projectedCards != 1 {
		t.Fatalf("answer/card counts = %d/%d", answeredMessages, projectedCards)
	}
}
