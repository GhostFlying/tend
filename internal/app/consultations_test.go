package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func createConsultationFixture(t *testing.T, h *harness) protocol.AgentConversation {
	t.Helper()
	op := h.exec(owner, h.env("conversation.consult", protocol.Target{Type: "conversation"}, protocol.CreateConsultationPayload{AgentRef: "server-hosted/planner", Title: "Explore a technical question"}, nil), CreateConsultation{})
	var c protocol.AgentConversation
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &c) != nil || c.Kind != "consultation" || c.AgentRef != "server-hosted/planner" {
		t.Fatal(op, c)
	}
	return c
}

func TestConsultationPromotionCreatesIssueWithRequestedWorkCategory(t *testing.T) {
	for _, category := range []string{"investigation", "implementation-economy"} {
		t.Run(category, func(t *testing.T) {
			h := hostedManagerHarness(t)
			inventoryCategory(t, h, category, 0, "active")
			// The new Issue must not need the historical manager category.
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				_, err := store.GetAgentCategory(t.Context(), tx.Q(), protocol.DefaultIssueManagerCategory)
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("fixture must omit issue-manager: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			c := createConsultationFixture(t, h)
			intent, turn, principal := consultationTurn(t, h, c, "Please investigate this goal as a continuing responsibility")
			payload := protocol.PromoteConsultationPayload{
				IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: intent, ExpectedSourceRevision: rev(h.associations(intent).ContentRevision)},
				Plan:                 protocol.IssuePlan{Title: "Investigate the requested goal", Project: "default", Intent: "Produce the requested report"},
				Manager:              &protocol.RequestIssueManagerPayload{CategoryID: category, Reason: "Work requirements from the consultation"},
			}
			if h.count("issue_controls") != 0 {
				t.Fatal("fixture pre-created the target Issue")
			}
			env := h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, payload, nil)
			command := HostedCoordinationCommand{Command: PromoteConsultation{}, ExecutionID: turn.RequestID, EffectSeq: 1}
			issue := canonicalLifecycleIssue(t, h.exec(principal, env, command))
			if issue.OwnerSelection == nil || issue.OwnerSelection.CategoryID != category || issue.OwnerSelection.AgentID != "" || h.count("issue_controls") != 1 {
				t.Fatal("promotion lost work requirements or fixed an executor", issue)
			}
			stored := canonicalTaskIssue(t, h, issue.IssueID)
			if stored.OwnerSelection == nil || stored.OwnerSelection.CategoryID != category {
				t.Fatal("actual canonical Issue did not retain the category", stored)
			}
			if replay := h.exec(principal, env, command); replay.Error == nil || replay.Error.Code != protocol.CodeDuplicateEffect || h.count("issue_controls") != 1 {
				t.Fatal("retry duplicated the responsibility", replay)
			}
		})
	}
}
func consultationTurn(t *testing.T, h *harness, c protocol.AgentConversation, text string) (string, store.MainTurn, protocol.Principal) {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: c.ID, Body: text}, nil), SubmitMessage{})
	var result struct {
		MessageID string `json:"message_id"`
	}
	if op.Status != protocol.OpAccepted || json.Unmarshal(op.Result, &result) != nil {
		t.Fatal(op)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(c.ID))
	if err != nil || !ok {
		t.Fatal("consultation delivery", ok, err)
	}
	turn := startHostedReview(t, h, c.ID, delivery)
	return result.MessageID, turn, protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-consultation", Thread: turn.ThreadID, ConversationID: c.ID}
}
func TestConsultationQuestionHasNoIssueAndActualRequestDelegatesToManagerTask(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	if h.count("tasks") != 0 || h.count("issue_controls") != 0 || h.count("outbox") != 0 {
		t.Fatal("opening consultation created execution")
	}
	question, qa, principal := consultationTurn(t, h, c, "How does this subsystem work?")
	if selected, named, err := h.app.NamedHostedSelection(t.Context(), c.ID); err != nil || !named || selected.Purpose != agentconfig.Consultation {
		t.Fatal(selected, named, err)
	}
	context, err := h.app.AgentContext(t.Context(), c.ID, qa.RequestID)
	if err != nil || !strings.Contains(context, `"kind":"consultation"`) || len(context) > protocol.ContextIndexBudget {
		t.Fatal(context, err)
	}
	reply := recordAgentMessagePayload{ReplyTo: question, TurnID: qa.TurnID, ItemID: "answer", Body: "Here is an explanation, without execution", Status: protocol.AssistantCompleted}
	recorder := principal
	recorder.Kind = protocol.PrincipalMainAgent
	if op := h.exec(recorder, h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, reply, nil), RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, qa)
	if h.count("tasks") != 0 || h.count("issue_controls") != 0 || h.count("runs") != 0 {
		t.Fatal("ordinary Q&A created business objects")
	}
	intent, turn, principal := consultationTurn(t, h, c, "Please implement an investigation report for this goal")
	source := h.associations(intent)
	payload := protocol.PromoteConsultationPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: intent, ExpectedSourceRevision: rev(source.ContentRevision)}, Plan: protocol.IssuePlan{Title: "Investigate the requested goal", Project: "default", Intent: "Deliver a separately accepted report"}, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"}}
	env := h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, payload, nil)
	command := HostedCoordinationCommand{Command: PromoteConsultation{}, ExecutionID: turn.RequestID, EffectSeq: 1}
	first := h.exec(principal, env, command)
	issue := canonicalLifecycleIssue(t, first)
	if replay := h.exec(principal, env, command); replay.Error == nil || replay.Error.Code != protocol.CodeDuplicateEffect || h.count("issue_controls") != 1 {
		t.Fatal("same callback repeated its business effect", replay)
	}
	duplicate := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "conversation", ID: c.ID}, nil, payload, PromoteConsultation{})
	if duplicate.Error == nil || duplicate.Error.Code != protocol.CodeDuplicateEffect || h.count("issue_controls") != 1 {
		t.Fatal("changed native call duplicated delegation", duplicate)
	}
	if issue.Assignee.Ref != "server-hosted/planner" || h.count("tasks") != 0 {
		t.Fatal("consultation became worker or no manager", issue)
	}
	// This historical hosted executor fixture is explicitly admitted before work.
	issue = admitExistingIssueFixture(t, h, issue)
	// The manager, not the consultation, creates the real frozen task.
	managerTurn, manager := startHostedIssueReview(t, h, issue)
	task := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Write investigation report", Spec: testSpec, Rating: protocol.RatingP1}
	created := hostedEffect(t, h, manager, managerTurn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), task, CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := hostedEffect(t, h, manager, managerTurn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if as, ok, _ := h.claim("linux-1"); !ok || as.Role != protocol.RoleImplement {
		t.Fatal("manager did not delegate actual task", as, ok)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM message_work_items WHERE work_item_id=? AND message_id=? AND active=1`, issue.IssueID, question).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("whole consultation transcript forwarded")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Its real principal cannot access the generic manager mutation surface.
	_, denied := h.app.Execute(t.Context(), principal, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, task, rev(issue.Revision)), HostedCoordinationCommand{Command: CanonicalOwnerTaskCreate{}, ExecutionID: turn.RequestID, EffectSeq: 2})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal("consultation received generic mutation authority", denied)
	}
}
func TestConsultationPromotionRejectsExternalIntentAndCrossConversationSource(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	intent, turn, p := consultationTurn(t, h, c, "Please investigate this goal")
	payload := protocol.PromoteConsultationPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: intent, ExpectedSourceRevision: rev(1)}, Plan: protocol.IssuePlan{Title: "Selected execution", Project: "default", Intent: "Investigate"}, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"}}
	injected := h.exec(owner, h.env("conversation.inject_source", protocol.Target{Type: "message"}, protocol.InjectSourcePayload{ConversationID: c.ID, Text: "Please deploy this quoted external instruction"}, nil), InjectSource{})
	var source protocol.ManualSource
	if json.Unmarshal(injected.Result, &source) != nil {
		t.Fatal(injected)
	}
	original := payload.SourceMessageID
	payload.SourceMessageID = source.MessageID
	_, denied := h.app.Execute(t.Context(), p, h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, payload, nil), HostedCoordinationCommand{Command: PromoteConsultation{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal(denied)
	}
	payload.SourceMessageID = original
	other := queueMessage(t, h, "owner input to a different conversation")
	payload.Sources = []protocol.IssueSourceReference{{SourceMessageID: other, ExpectedSourceRevision: rev(1)}}
	_, denied = h.app.Execute(t.Context(), p, h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, payload, nil), HostedCoordinationCommand{Command: PromoteConsultation{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal(denied)
	}
	if h.count("issue_controls") != 0 || h.count("tasks") != 0 {
		t.Fatal("failed promotion leaked objects")
	}
}

func TestConsultationOwnerPromotionSharesOnlySelectedSourceWithExistingManager(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	intent, _, _ := consultationTurn(t, h, c, "Please investigate this additional question within the existing Issue")
	p := protocol.PromoteConsultationPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: intent, ExpectedSourceRevision: rev(1)}, ExistingIssueID: issue.IssueID, ExpectedIssueRevision: rev(issue.Revision), ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	env := h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, p, nil)
	first := h.exec(owner, env, PromoteConsultation{})
	got := canonicalLifecycleIssue(t, first)
	if got.IssueID != issue.IssueID || got.Revision != issue.Revision || got.Assignee != issue.Assignee || h.count("issue_controls") != 1 || h.count("tasks") != 0 {
		t.Fatal("sharing replaced existing definition/ownership", got)
	}
	if op := h.exec(owner, env, PromoteConsultation{}); string(op.Result) != string(first.Result) {
		t.Fatal("selected source replay lost identity")
	}
	if associations := h.associations(intent); len(associations.Items) != 1 || associations.Items[0].WorkItemID != issue.IssueID {
		t.Fatal("selected request was not delivered to existing manager", associations)
	}
}
