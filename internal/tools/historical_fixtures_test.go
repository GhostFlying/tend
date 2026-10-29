package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
)

// historicalToolFixture invokes a retired internal handler solely to construct
// or validate historical ledger facts in tests. It does not register a public
// capability, and ordinary call/Handle tests never fall back to it.
func (e *env) historicalToolFixture(name string, args any, callID string) answerView {
	e.t.Helper()
	handlers := map[string]func(*Service, context.Context, Call) (any, error){
		"work_item_context_read":       (*Service).contextRead,
		"work_item_context_prepare":    (*Service).contextPrepare,
		"work_item_context_fact":       (*Service).contextFact,
		"work_items_followups_list":    (*Service).followupsList,
		"work_items_followups_history": (*Service).followupsHistory,
		"work_items_followups_create":  (*Service).followupsCreate,
		"work_items_followups_update":  (*Service).followupsUpdate,
		"work_items_followups_pause":   (*Service).followupsPause,
		"work_items_followups_resume":  (*Service).followupsResume,
		"work_items_followups_cancel":  (*Service).followupsCancel,
		"work_items_followups_trigger": (*Service).followupsTrigger,
		"work_items_refresh_delivery":  (*Service).workItemsRefresh,
		"work_item_choices_list":       (*Service).workItemChoicesList,
		"work_item_choices_create":     (*Service).workItemChoicesCreate,
		"work_item_choices_resolve":    (*Service).workItemChoicesResolve,
		"work_item_choices_withdraw":   (*Service).workItemChoicesWithdraw,
		"work_item_choices_replace":    (*Service).workItemChoicesReplace,
		"work_items_list":              (*Service).workItemsList,
		"work_items_read":              (*Service).workItemsRead,
		"work_items_create":            (*Service).workItemsCreate,
		"work_items_update":            (*Service).workItemsUpdate,
		"work_items_close":             (*Service).workItemsClose,
		"work_items_reopen":            (*Service).workItemsReopen,
		"work_items_link_task":         (*Service).workItemsLink,
		"tasks_create":                 (*Service).tasksCreate,
		"tasks_update_intent":          (*Service).tasksUpdateIntent,
	}
	run, ok := handlers[name]
	if !ok {
		e.t.Fatalf("not a retired historical fixture tool: %s", name)
	}
	if callID == "" {
		e.calls++
		callID = "historical_" + string(rune('a'+e.calls))
	}
	raw, err := json.Marshal(args)
	if err != nil {
		e.t.Fatal(err)
	}
	c := Call{Principal: hosted, Native: "th_1/tu_1/" + callID, SourceMessageID: "msg_1", Tool: name, Arguments: raw}
	value, err := run(e.svc, context.Background(), c)
	a := answer{OK: err == nil, Result: value}
	if err != nil {
		var pe *protocol.Error
		if !errors.As(err, &pe) {
			e.t.Fatal(err)
		}
		a.Error = pe
		if pe.Code == protocol.CodeStaleRevision {
			a.Current = e.svc.currentFor(context.Background(), c)
		}
	}
	data, err := json.Marshal(a)
	if err != nil {
		e.t.Fatal(err)
	}
	var out answerView
	if err := json.Unmarshal(data, &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}
