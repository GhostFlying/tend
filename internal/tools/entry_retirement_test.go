package tools

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetiredLegacyPlanningToolsRejectActualCalls(t *testing.T) {
	e := newEnv(t)
	for _, spec := range Specs() {
		if strings.HasPrefix(spec.Name, "work_items_") || strings.HasPrefix(spec.Name, "work_item_") || spec.Name == "tasks_create" || spec.Name == "tasks_update_intent" {
			t.Errorf("retired planning tool still advertised: %s", spec.Name)
		}
	}
	for _, name := range []string{"work_items_create", "work_items_update", "work_items_close", "work_items_reopen", "work_items_link_task", "work_item_context_read", "work_item_context_prepare", "work_item_context_fact", "work_item_choices_create", "work_item_choices_replace", "work_items_followups_create", "work_items_refresh_delivery", "tasks_create", "tasks_update_intent"} {
		answer := e.call(name, createArgs{Title: "Must not create old task", Spec: spec, CommitSeq: 1}, "")
		if answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(answer.Error.Message, "there is no tool") {
			t.Errorf("retired %s still dispatches: %+v", name, answer)
		}
	}
	if e.tasks() != 0 {
		t.Fatal("retired entry created task")
	}
}
