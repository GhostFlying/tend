package mainthread

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestSteerPlanningNewCallIDRetriesKeepTurnIdentity(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return ""
	}
	inputs := make(chan harness.TurnInput, 1)
	e := newEnv(t, h, withSteer(h, func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
		inputs <- in
		return nil
	}))
	first := e.say("Create one task for the approved goal")
	th := <-started
	var turnID string
	eventually(t, "recorded native turn", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		turnID = e.m.active.turnID
		return turnID != "" && e.count(`SELECT COUNT(*) FROM main_turns WHERE turn_id=?`, turnID) == 1
	})
	args := func(seq int, source string) json.RawMessage {
		p := map[string]any{"title": "approved task", "spec": spec, "commit_seq": seq}
		if source != "" {
			p["source_message_id"] = source
		}
		b, _ := json.Marshal(p)
		return b
	}
	call := harness.ToolCall{ThreadID: th.ID(), TurnID: turnID, CallID: "original", Tool: "tasks_create", Arguments: args(1, "")}
	result := e.m.onToolCall(t.Context(), call)
	if !result.Success {
		t.Fatal(result.Text)
	}
	var original struct {
		Result struct {
			RequestID string `json:"request_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Text), &original); err != nil || original.Result.RequestID == "" {
		t.Fatalf("original request: %s, %v", result.Text, err)
	}
	duplicate := func(m *Manager, id string) {
		t.Helper()
		call.CallID = id
		res := m.onToolCall(t.Context(), call)
		var reply struct {
			Error *protocol.Error `json:"error"`
		}
		if err := json.Unmarshal([]byte(res.Text), &reply); err != nil || res.Success || reply.Error == nil || reply.Error.Code != protocol.CodeDuplicateEffect {
			t.Fatalf("logical retry %s: %s, %v", id, res.Text, err)
		}
		var details struct {
			RequestID string `json:"original_request_id"`
		}
		if err := json.Unmarshal(reply.Error.Details, &details); err != nil || details.RequestID != original.Result.RequestID {
			t.Fatalf("retry lost original committed outcome: %s", res.Text)
		}
	}
	duplicate(e.m, "lost-result-before-steer")
	second := e.say("Status only: report progress, preserve the goal; no new task is authorized")
	in := <-inputs
	// Reconcile the actual native snapshot directly in the callback before
	// the event pump catches up, as the real adapter can do at a safe boundary.
	call.UserText, call.UserItemID = in.Text, "status-user"
	duplicate(e.m, "lost-result-after-steer")
	call.Arguments = args(1, first)
	duplicate(e.m, "explicit-original-source")
	call.Arguments = args(1, second)
	duplicate(e.m, "explicit-status-source")
	// Recovery has no active in-memory turn/source. Persisted initial
	// delivery and consumption still catch logical retries under new IDs.
	recovered := New(e.app, e.m.context, e.m.tools, e.m.cfg)
	duplicate(recovered, "lost-result-after-recovery")
	if e.count(`SELECT COUNT(*) FROM tasks`) != 1 || e.count(`SELECT COUNT(*) FROM plan_commits`) != 1 {
		t.Fatal("status steer duplicated planning effect")
	}
	// A later authorized planning commit keeps the actual owner audit source,
	// while still sharing the original turn's sequence namespace.
	third := e.say("Also create a second task for the same approved goal")
	in = <-inputs
	call.UserText, call.UserItemID = in.Text, "authorization-user"
	call.CallID, call.Arguments = "new-plan", args(2, "")
	if res := e.m.onToolCall(t.Context(), call); !res.Success {
		t.Fatal(res.Text)
	}
	if e.count(`SELECT COUNT(*) FROM tasks`) != 2 ||
		e.count(`SELECT COUNT(*) FROM plan_commits WHERE source_message_id=? AND commit_seq=2`, first) != 1 ||
		e.count(`SELECT COUNT(*) FROM conversation_bindings WHERE message_id=? AND object_type='task'`, third) != 1 ||
		e.count(`SELECT COUNT(*) FROM request_records r JOIN main_steers s ON s.request_id=r.parent_request_id WHERE s.message_id=? AND r.operation='tasks.create' AND r.status='succeeded'`, third) != 1 {
		t.Fatal("new planning effect lost stable identity or actual owner audit source")
	}
	call.Tool = "tasks_update_intent"
	var taskID string
	// Read the task bound to the first owner message for a valid cross-tool
	// retry. Reusing commit_seq is forbidden across planning operations too.
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT object_id FROM conversation_bindings WHERE message_id=? AND object_type='task'`, first).Scan(&taskID)
	}); err != nil {
		t.Fatal(err)
	}
	call.Arguments, _ = json.Marshal(map[string]any{"task_id": taskID, "expected_control_revision": 1, "rating": "p2", "commit_seq": 1})
	duplicate(e.m, "cross-tool-sequence-retry")
	call.Tool = "work_items_create"
	plan := protocol.IssuePlan{Title: "approved goal", Intent: "keep the continuing concern"}
	call.Arguments, _ = json.Marshal(map[string]any{"plan": plan, "commit_seq": 1})
	duplicate(e.m, "work-item-sequence-retry")
	call.CallID = "new-work-item-plan"
	call.Arguments, _ = json.Marshal(map[string]any{"plan": plan, "commit_seq": 3, "initial_context": protocol.InitialWorkItemContext{Coverage: "Reviewed the approved owner goal", ReviewedKinds: []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}, SourceMessageIDs: []string{third}, Facts: []protocol.ContextFact{{Kind: "goal", Scope: "work_item", Body: "Create the approved work item", SourceMessageID: third, Attribution: "owner"}}}})
	created := e.m.onToolCall(t.Context(), call)
	var workItemResult struct {
		Result struct {
			Outcome protocol.WorkItem `json:"outcome"`
		} `json:"result"`
	}
	if !created.Success || json.Unmarshal([]byte(created.Text), &workItemResult) != nil || workItemResult.Result.Outcome.WorkItemID == "" {
		t.Fatal(created.Text)
	}
	item := workItemResult.Result.Outcome.WorkItemID
	if e.count(`SELECT COUNT(*) FROM plan_commits WHERE source_message_id=? AND commit_seq=3`, first) != 1 ||
		e.count(`SELECT COUNT(DISTINCT object_id) FROM conversation_bindings WHERE message_id=? AND object_type='work_item' AND object_id=?`, third, item) != 1 ||
		e.count(`SELECT COUNT(*) FROM logical_turn_contexts WHERE logical_turn_id=(SELECT request_id FROM main_turns WHERE message_id=?) AND work_item_id=?`, first, item) != 1 {
		t.Fatal("work item lost stable planning identity, owner source or logical review")
	}
}
