package mainthread

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Planning is now Issue delegation. Its durable identity is execution+effect,
// independent of the latest owner input or a retried native call ID.
func TestSteerCoordinationRetriesKeepExecutionIdentity(t *testing.T) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "named-model", Name: "native-named", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	started := make(chan *testkit.FakeThread, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, _ string, _ harness.TurnInput) string {
		started <- th
		<-release
		return ""
	}
	inputs := make(chan harness.TurnInput, 2)
	e := newNamedSteerFixture(t, h, inputs)
	first := e.say("Delegate the approved goal to me as its responsible owner")
	th := <-started
	var turnID, execution string
	eventually(t, "named execution recorded", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		if e.m.active != nil {
			turnID, execution = e.m.active.turnID, e.m.active.requestID
		}
		return turnID != "" && execution != ""
	})
	payload := func(source, title string) protocol.CreateIssuePayload {
		rev := int64(1)
		return protocol.CreateIssuePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: &rev}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Plan: protocol.IssuePlan{Title: title, Project: "default", Intent: "Preserve exactly this delegated goal"}}
	}
	raw, _ := json.Marshal(map[string]any{"effect_seq": 1, "payload": payload(first, "First responsibility")})
	call := harness.ToolCall{ThreadID: th.ID(), TurnID: turnID, CallID: "original", Tool: "issues_create", Arguments: raw}
	result := e.m.onToolCall(t.Context(), call)
	var original struct {
		Result struct {
			RequestID string `json:"request_id"`
		} `json:"result"`
	}
	if !result.Success || json.Unmarshal([]byte(result.Text), &original) != nil || original.Result.RequestID == "" {
		t.Fatal(result.Text)
	}
	duplicate := func(m *Manager, id string) {
		t.Helper()
		call.CallID = id
		res := m.onToolCall(t.Context(), call)
		var reply struct {
			Error *protocol.Error `json:"error"`
		}
		if res.Success || json.Unmarshal([]byte(res.Text), &reply) != nil || reply.Error == nil || reply.Error.Code != protocol.CodeDuplicateEffect {
			t.Fatalf("retry %s: %s", id, res.Text)
		}
		var details struct {
			RequestID string `json:"original_request_id"`
		}
		if json.Unmarshal(reply.Error.Details, &details) != nil || details.RequestID != original.Result.RequestID {
			t.Fatalf("original receipt lost: %s", res.Text)
		}
	}
	duplicate(e.m, "lost-before-steer")
	second := e.say("Status only; preserve the existing responsibility")
	in := <-inputs
	call.UserText, call.UserItemID = in.Text, "status-user"
	duplicate(e.m, "lost-after-steer")
	recovered := New(e.app, e.m.context, e.m.tools, e.m.cfg)
	duplicate(recovered, "lost-after-recovery")
	if e.count(`SELECT COUNT(*) FROM issue_controls`) != 1 || e.count(`SELECT COUNT(*) FROM hosted_coordination_effects WHERE execution_id=?`, execution) != 1 || e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='consumed'`, second) != 1 {
		t.Fatal("status steer duplicated effect or lost consumption")
	}
	third := e.say("Also delegate one additional responsibility to me")
	in = <-inputs
	call.UserText, call.UserItemID = in.Text, "authorization-user"
	call.CallID = "next-effect"
	call.Arguments, _ = json.Marshal(map[string]any{"effect_seq": 2, "payload": payload(third, "Second responsibility")})
	if res := e.m.onToolCall(t.Context(), call); !res.Success {
		t.Fatal(res.Text)
	}
	if e.count(`SELECT COUNT(*) FROM issue_controls`) != 2 || e.count(`SELECT COUNT(*) FROM hosted_coordination_effects WHERE execution_id=?`, execution) != 2 || e.count(`SELECT COUNT(*) FROM conversation_bindings WHERE message_id=? AND object_type='work_item'`, third) != 1 {
		t.Fatal("new effect lost original execution or actual owner source")
	}
	call.Tool = "conversation_associate_issues"
	// A different valid operation cannot reuse a committed effect sequence.
	var associations protocol.MessageAssociations
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		associations, err = store.MessageAssociations(t.Context(), tx.Q(), third)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	call.Arguments, _ = json.Marshal(map[string]any{"effect_seq": 1, "target_id": third, "expected_control_revision": associations.Revision, "payload": map[string]any{"disposition": "linked", "content_revision": associations.ContentRevision, "issue_ids": []string{associations.Items[0].WorkItemID}}})
	duplicate(e.m, "cross-tool-effect-retry")
}
