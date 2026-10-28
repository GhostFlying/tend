package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func queuedTerminalShareFixture(t *testing.T, terminal string) (*harness, protocol.Issue, string, store.MainTurn) {
	t.Helper()
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, map[string]string{"body": "Review this scope when available"}, nil), SubmitMessage{})
	var source struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submitted.Result, &source); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(owner, source.MessageID, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if terminal == "cancelled" {
		issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	}
	endpoint := canonicalLifecycleSourceEndpoint(t, h, issue, source.MessageID)
	action := "finish_cancel"
	if terminal == "closed" {
		action = "close"
		endpoint.Closure = canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted).Closure
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, action, endpoint))
	// Neither the original owner delivery nor its association notice has registered
	// a native/logical turn yet. The source is real and remains associated.
	if h.count("logical_turns") != 0 {
		t.Fatal("source settlement was not queued ahead of registration")
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim queued source: %v %v", ok, err)
	}
	var message HandleMessage
	if err := json.Unmarshal(delivery.Payload, &message); err != nil {
		t.Fatal(err)
	}
	if message.MessageID != source.MessageID {
		t.Fatalf("claimed %s instead of original source %s", message.MessageID, source.MessageID)
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: source.MessageID, ThreadID: mainAgent.Thread, TurnID: "late-registration", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	return h, issue, source.MessageID, turn
}

func exactTerminalShareCommit(t *testing.T, h *harness, request string) protocol.Operation {
	t.Helper()
	state, err := h.app.LogicalTurn(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	payload := protocol.LogicalTurnCommit{Contexts: state.Contexts}
	for _, message := range state.Messages {
		payload.Messages = append(payload.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
	}
	return h.exec(mainAgent, h.env("conversation.commit_turn", protocol.Target{Type: "logical_turn", ID: request}, payload, nil), CommitLogicalTurn{RequestID: request})
}

func TestCanonicalLifecycleLateRegisteredTerminalShareExplicitReview(t *testing.T) {
	for _, terminal := range []string{"closed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			h, issue, _, turn := queuedTerminalShareFixture(t, terminal)
			op := exactTerminalShareCommit(t, h, turn.RequestID)
			if op.Status != protocol.OpSucceeded {
				t.Fatalf("late terminal share review=%+v", op)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var baseline int64
				var reviewed bool
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&baseline); err != nil {
					return err
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_context_reviews r JOIN work_item_contexts c USING(work_item_id) WHERE r.logical_turn_id=? AND r.work_item_id=? AND r.revision=c.revision)`, turn.RequestID, issue.IssueID).Scan(&reviewed); err != nil {
					return err
				}
				if baseline != 0 || !reviewed {
					t.Fatalf("baseline=%d exact share reviewed=%v", baseline, reviewed)
				}
				native, err := store.GetMainTurn(t.Context(), tx.Q(), turn.RequestID)
				if err != nil {
					return err
				}
				if native.Status != store.TurnStarted {
					t.Fatal("fact review fabricated native completion")
				}
				saved, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
				if err != nil {
					return err
				}
				a, _ := json.Marshal(saved)
				b, _ := json.Marshal(issue)
				if string(a) != string(b) {
					t.Fatal("share review changed terminal responsibility")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
			if err != nil || state.Status == "completed" {
				t.Fatalf("native still active: %+v %v", state, err)
			}
			turn.Status = store.TurnCompleted
			if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			state, err = h.app.LogicalTurn(t.Context(), turn.RequestID)
			if err != nil || state.Status != "completed" || state.RepairAttempts != 0 {
				t.Fatalf("settled=%+v %v", state, err)
			}
		})
	}
}

func TestCanonicalLifecycleLateRegisteredShareStillRejectsNewResponsibility(t *testing.T) {
	for _, change := range []string{"reopen", "late-content"} {
		t.Run(change, func(t *testing.T) {
			h, issue, source, turn := queuedTerminalShareFixture(t, "closed")
			if change == "reopen" {
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
			} else {
				report := recordAgentMessagePayload{Body: "Initial late report", ReplyTo: source, TurnID: turn.TurnID, ItemID: "late-terminal-report", Status: protocol.AssistantStreaming}
				result := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
				if result.Status != protocol.OpSucceeded {
					t.Fatal(result)
				}
				var message struct {
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(result.Result, &message); err != nil {
					t.Fatal(err)
				}
				if op := h.associate(owner, message.MessageID, issue.IssueID); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "review_sources", canonicalLifecycleSourceEndpoint(t, h, issue, message.MessageID)))
				report.Body = "Late report now contains changed facts"
				report.Status = protocol.AssistantCompleted
				result = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
				if result.Status != protocol.OpSucceeded {
					t.Fatal(result)
				}
				if op := h.associate(owner, message.MessageID, issue.IssueID); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			expectWorkCode(t, exactTerminalShareCommit(t, h, turn.RequestID), protocol.CodeInvalidTransition)
			state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
			if err != nil || state.Status == "completed" {
				t.Fatalf("new responsibility disappeared: %+v %v", state, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var baseline int64
				var reviewed bool
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&baseline); err != nil {
					return err
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_context_reviews r JOIN work_item_contexts c USING(work_item_id) WHERE r.logical_turn_id=? AND r.work_item_id=? AND r.revision=c.revision)`, turn.RequestID, issue.IssueID).Scan(&reviewed); err != nil {
					return err
				}
				if baseline != 0 || reviewed {
					t.Fatal("new source/reopen gained a current terminal review")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
