package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func delegationPayload(source string) protocol.CreateIssuePayload {
	return protocol.CreateIssuePayload{
		IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source},
		Plan:                 protocol.IssuePlan{Title: "Repair the mobile UI", Project: "default", Intent: "Reproduce the owner's screenshots and verify the repair"},
		Assignee:             &protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject},
	}
}

func TestOwnerDelegationPreservesSourceFailureAndPublishesOrdinaryResponsibility(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Delegate this goal to a suitable ordinary Agent")
	turn := startHostedReview(t, h, "main", delivery)
	payload := delegationPayload(source)
	payload.Assignee = nil
	payload.Owner = &protocol.IssueOwnerRequest{CategoryID: "planner", Reason: "Accept the continuing responsibility"}
	cmd := CanonicalOwnerIssueCommand{Action: "create"}
	bad := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, payload, cmd)
	expectWorkCode(t, bad, protocol.CodeInvalidRequest)
	payload.ExpectedSourceRevision = rev(1)
	good := hostedEffect(t, h, mainAgent, turn, 2, protocol.Target{Type: "issue"}, nil, payload, cmd)
	if good.Status != protocol.OpSucceeded {
		t.Fatal(good)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(good.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		manager, err := store.CurrentIssueManagerTask(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		raw, err := store.GetSpec(t.Context(), tx.Q(), manager.TaskID, 1)
		if err != nil {
			return err
		}
		var spec protocol.IssueManagerSpec
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			return err
		}
		if spec.CategoryID != "planner" || spec.OwnershipPolicy != protocol.IssueOwnerPolicy || manager.Pin != nil {
			t.Fatal("publication invented a manager identity or required a special category", spec, manager)
		}
		reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
		if err == nil && reason != "" {
			t.Fatal("corrected source did not discharge this exact owner delegation", reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func unconsumedDelegationSource(t *testing.T, h *harness) string {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "A separate input"}, nil), SubmitMessage{})
	var result struct {
		MessageID string `json:"message_id"`
	}
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || result.MessageID == "" {
		t.Fatal(op, err)
	}
	return result.MessageID
}

func TestDelegationFailureRemainsBlockedAfterMessageReview(t *testing.T) {
	for _, sourceKind := range []string{"missing_revision", "missing_source", "revision_in_id", "unadmitted_source"} {
		t.Run(sourceKind, func(t *testing.T) {
			h := hostedManagerHarness(t)
			id, delivery := h.say("Repair these three PWA problems")
			turn := startHostedReview(t, h, "main", delivery)
			p := delegationPayload(id)
			switch sourceKind {
			case "missing_source":
				p.SourceMessageID = ""
			case "revision_in_id":
				p.SourceMessageID += "@1"
			case "unadmitted_source":
				p.SourceMessageID = unconsumedDelegationSource(t, h)
			}
			cmd := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1}
			env := h.env(cmd.Operation(), protocol.Target{Type: "issue"}, p, nil)
			op := h.exec(mainAgent, env, cmd)
			if op.Status != protocol.OpFailed {
				t.Fatal(op)
			}
			if sourceKind == "missing_revision" && (op.Error == nil || !strings.Contains(op.Error.Message, "expected_source_revision")) {
				t.Fatal(op)
			}
			events := h.count("events")
			replay := h.exec(mainAgent, env, cmd)
			if replay.Status != op.Status || h.count("coordination_delegation_attempts") != 1 || h.count("events") != events {
				t.Fatal("failure replay changed responsibility", replay)
			}
			if h.count("issue_controls") != 0 || h.count("tasks") != 0 {
				t.Fatal("rejected delegation created business work")
			}
			if op := h.associate(mainAgent, id); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			commitFixtureTurn(t, h, turn.RequestID)
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
			if err != nil || state.Status != "needs_review" || !strings.Contains(state.Reason, "delegation") || state.RepairAttempts != 0 {
				t.Fatal(state, err)
			}
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if h.count("coordination_delegation_attempts") != 1 {
				t.Fatal("settlement duplicated attempts")
			}
			resume := h.exec(owner, h.env("conversation.resume_processing", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, rev(h.associations(id).Processing.Revision)), ResumeLogicalTurn{})
			expectWorkCode(t, resume, protocol.CodeInvalidTransition)
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				m, err := messagingMessage(t.Context(), tx.Q(), id)
				if err == nil && m.Processing != "needs_review" {
					t.Fatalf("IM hides blocked work: %+v", m)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCorrectedDelegationResolvesOnlyItsExactSource(t *testing.T) {
	for _, correction := range []string{"same_source", "other_source"} {
		t.Run(correction, func(t *testing.T) {
			h := hostedManagerHarness(t)
			id, delivery := h.say("Repair the PWA")
			turn := startHostedReview(t, h, "main", delivery)
			p := delegationPayload(id)
			cmd := CanonicalOwnerIssueCommand{Action: "create"}
			bad := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd)
			expectWorkCode(t, bad, protocol.CodeInvalidRequest)
			source := id
			if correction == "other_source" {
				source = unconsumedDelegationSource(t, h)
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, turn.RequestID, source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			p.SourceMessageID = source
			p.ExpectedSourceRevision = rev(1)
			good := hostedEffect(t, h, mainAgent, turn, 2, protocol.Target{Type: "issue"}, nil, p, cmd)
			if good.Status != protocol.OpSucceeded {
				t.Fatal(good)
			}
			if h.count("issue_controls") != 1 || h.count("coordination_delegation_attempts") != 2 {
				t.Fatal("correction lost either failure or success")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
				if err == nil && (reason == "") != (correction == "same_source") {
					t.Fatalf("correction=%s reason=%q", correction, reason)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDelegationFailureReceiptAndResponsibilityCommitTogether(t *testing.T) {
	h := hostedManagerHarness(t)
	id, delivery := h.say("Delegate repair")
	turn := startHostedReview(t, h, "main", delivery)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_delegation_receipt BEFORE INSERT ON coordination_delegation_attempts BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cmd := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1}
	env := h.env(cmd.Operation(), protocol.Target{Type: "issue"}, delegationPayload(id), nil)
	_, err := h.app.Execute(t.Context(), mainAgent, env, cmd)
	if err == nil {
		t.Fatal("injected error was ignored")
	}
	if h.count("coordination_delegation_attempts") != 0 {
		t.Fatal("partial responsibility persisted")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM request_records WHERE request_id=?`, env.RequestID).Scan(&n)
		if n != 0 {
			t.Fatal("failure receipt committed without responsibility")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerInputLeavesRecordFieldsToServer(t *testing.T) {
	h := newHarness(t)
	input := protocol.PlannerEntryInput{Kind: "user_action", Title: "Review the blocked request", Responsible: owner.Subject, Rank: 1, Status: "blocked", Reason: "Delegation is not yet accepted"}
	op := h.exec(owner, h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, protocol.PutPlannerEntryPayload{Entry: input.Entry()}, nil), PutPlannerEntry{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var entry protocol.PlannerEntry
	if err := json.Unmarshal(op.Result, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.EntryID == "" || entry.Revision != 1 || entry.UpdatedAt != h.clock.Now().UnixMilli() {
		t.Fatal(entry)
	}
	input.Status = "open"
	update := h.exec(owner, h.env("planner.entries_put", protocol.Target{Type: "planner_entry", ID: entry.EntryID}, protocol.PutPlannerEntryPayload{Entry: input.Entry()}, rev(entry.Revision)), PutPlannerEntry{})
	if update.Status != protocol.OpSucceeded {
		t.Fatal(update)
	}
	if err := json.Unmarshal(update.Result, &entry); err != nil || entry.Revision != 2 || entry.Status != "open" {
		t.Fatal(entry, err)
	}
}

func TestOneSourceMultipleIssueFailuresRemainIndependent(t *testing.T) {
	h := hostedManagerHarness(t)
	id, delivery := h.say("Repair the PWA and prepare a separate report")
	turn := startHostedReview(t, h, "main", delivery)
	cmd := CanonicalOwnerIssueCommand{Action: "create"}
	repair := delegationPayload(id)
	report := delegationPayload(id)
	report.Plan.Title = "Prepare the report"
	report.Plan.Intent = "Produce an independent report"
	for i, p := range []protocol.CreateIssuePayload{repair, report} {
		expectWorkCode(t, hostedEffect(t, h, mainAgent, turn, int64(i+1), protocol.Target{Type: "issue"}, nil, p, cmd), protocol.CodeInvalidRequest)
	}
	repair.ExpectedSourceRevision = rev(1)
	if op := hostedEffect(t, h, mainAgent, turn, 3, protocol.Target{Type: "issue"}, nil, repair, cmd); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
		if err == nil && !strings.HasPrefix(reason, "1 delegation") {
			t.Fatalf("repair erased the report failure: %q", reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestClarificationInheritsDelegationFailureAndItsCorrection(t *testing.T) {
	for _, correct := range []bool{false, true} {
		t.Run(map[bool]string{false: "unresolved", true: "corrected"}[correct], func(t *testing.T) {
			h := hostedManagerHarness(t)
			id, delivery := h.say("Repair the mobile PWA")
			turn := startHostedReview(t, h, "main", delivery)
			p := delegationPayload(id)
			expectWorkCode(t, hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, CanonicalOwnerIssueCommand{Action: "create"}), protocol.CodeInvalidRequest)
			wait := AwaitTurnClarification{RequestID: turn.RequestID}
			if op := h.exec(mainAgent, h.env(wait.Operation(), protocol.Target{Type: "logical_turn", ID: turn.RequestID}, protocol.TurnClarification{Question: "Which mobile browser?"}, nil), wait); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			answer, nextDelivery := h.say("Safari in PWA mode")
			next := startHostedReview(t, h, "main", nextDelivery)
			state, err := h.app.LogicalTurn(t.Context(), next.RequestID)
			if err != nil || len(state.Awaiting) != 1 {
				t.Fatal(state, err)
			}
			awaiting := state.Awaiting[0]
			adopt := AdoptClarification{RequestID: next.RequestID}
			op := h.exec(mainAgent, h.env(adopt.Operation(), protocol.Target{Type: "logical_turn", ID: awaiting.LogicalTurnID}, protocol.AdoptClarificationPayload{LogicalTurnID: awaiting.LogicalTurnID, Revision: awaiting.Revision}, rev(awaiting.Revision)), adopt)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if correct {
				p.ExpectedSourceRevision = rev(1)
				if op := hostedEffect(t, h, mainAgent, next, 2, protocol.Target{Type: "issue"}, nil, p, CanonicalOwnerIssueCommand{Action: "create"}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				reason, err := delegationBlockReason(t.Context(), tx.Q(), next.RequestID)
				if err == nil && (reason == "") != correct {
					t.Fatalf("corrected=%t inherited reason=%q", correct, reason)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !correct {
				h.associate(mainAgent, id)
				h.associate(mainAgent, answer)
				commitFixtureTurn(t, h, next.RequestID)
				next.Status = store.TurnCompleted
				if err := h.app.PutMainTurn(t.Context(), next); err != nil {
					t.Fatal(err)
				}
				state, err := h.app.LogicalTurn(t.Context(), next.RequestID)
				if err != nil || state.Status != "needs_review" {
					t.Fatal(state, err)
				}
			}
		})
	}
}

func TestMalformedDelegationRetainsItsDeterministicReceipt(t *testing.T) {
	for _, payload := range []string{`[]`, `true`, `"bad"`, `{"plan":[]}`} {
		t.Run(payload, func(t *testing.T) {
			h := hostedManagerHarness(t)
			_, delivery := h.say("Repair the PWA")
			turn := startHostedReview(t, h, "main", delivery)
			cmd := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1}
			env := h.env(cmd.Operation(), protocol.Target{Type: "issue"}, nil, nil)
			env.Payload = json.RawMessage(payload)
			op, err := h.app.Execute(t.Context(), mainAgent, env, cmd)
			if err != nil || op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
				t.Fatal(op, err)
			}
			replay, err := h.app.Execute(t.Context(), mainAgent, env, cmd)
			if err != nil || replay.Status != op.Status || h.count("coordination_delegation_attempts") != 1 {
				t.Fatal(replay, err)
			}
		})
	}
}

func TestNestedDelegationSourceFailureCannotBeClearedByAnotherSource(t *testing.T) {
	h := hostedManagerHarness(t)
	inventoryCategory(t, h, protocol.DefaultIssueManagerCategory, 0, "active")
	id, delivery := h.say("Repair the PWA")
	turn := startHostedReview(t, h, "main", delivery)
	other := unconsumedDelegationSource(t, h)
	p := delegationPayload(id)
	p.ExpectedSourceRevision = rev(1)
	p.Assignee = nil
	p.Manager = &protocol.RequestIssueManagerPayload{CategoryID: protocol.DefaultIssueManagerCategory, SourceMessageID: other, Reason: "Delegate the repair"}
	cmd := CanonicalOwnerIssueCommand{Action: "create"}
	bad := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd)
	expectWorkCode(t, bad, protocol.CodeForbidden)
	p.Manager.SourceMessageID = id
	good := hostedEffect(t, h, mainAgent, turn, 2, protocol.Target{Type: "issue"}, nil, p, cmd)
	if good.Status != protocol.OpSucceeded {
		t.Fatal(good)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
		if err == nil && reason == "" {
			t.Fatal("a different nested source erased the rejected source")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEquivalentCorrectedDelegationUsesCanonicalPlan(t *testing.T) {
	h := hostedManagerHarness(t)
	id, delivery := h.say("Repair the PWA")
	turn := startHostedReview(t, h, "main", delivery)
	p := delegationPayload(id)
	p.Plan.Project = ""
	cmd := CanonicalOwnerIssueCommand{Action: "create"}
	expectWorkCode(t, hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd), protocol.CodeInvalidRequest)
	p.ExpectedSourceRevision = rev(1)
	p.Plan.Project = "default"
	p.Plan.Stages = []protocol.IssueStage{}
	p.Plan.CompletionCriteria = []string{}
	if op := hostedEffect(t, h, mainAgent, turn, 2, protocol.Target{Type: "issue"}, nil, p, cmd); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
		if err == nil && reason != "" {
			t.Fatal("equivalent canonical plan stays blocked", reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationEffectCollisionRequiresTheSameSuccessfulIntention(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "different_intention", true: "same_intention"}[same], func(t *testing.T) {
			h := hostedManagerHarness(t)
			id, delivery := h.say("Repair the PWA and prepare a separate report")
			turn := startHostedReview(t, h, "main", delivery)
			p := delegationPayload(id)
			p.ExpectedSourceRevision = rev(1)
			cmd := CanonicalOwnerIssueCommand{Action: "create"}
			if op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if !same {
				p.Plan.Title = "Prepare the separate report"
				p.Plan.Intent = "Deliver a report independently of the repair"
			}
			expectWorkCode(t, hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd), protocol.CodeDuplicateEffect)
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
				if err == nil && (reason == "") != same {
					t.Fatalf("same=%t reason=%q", same, reason)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !same {
				if op := hostedEffect(t, h, mainAgent, turn, 2, protocol.Target{Type: "issue"}, nil, p, cmd); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
					if err == nil && reason != "" {
						t.Fatal("fresh corrected effect did not discharge the report", reason)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPreMigrationDelegationReceiptStillProvesExactDuplicate(t *testing.T) {
	h := hostedManagerHarness(t)
	id, delivery := h.say("Repair the PWA")
	turn := startHostedReview(t, h, "main", delivery)
	p := delegationPayload(id)
	p.ExpectedSourceRevision = rev(1)
	cmd := CanonicalOwnerIssueCommand{Action: "create"}
	if op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Model the empty journal added by migration to an existing request ledger.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM coordination_delegation_attempts`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd), protocol.CodeDuplicateEffect)
	if h.count("coordination_delegation_attempts") != 0 {
		t.Fatal("historical successful effect became a new failure")
	}
	if h.count("issue_controls") != 1 {
		t.Fatal("duplicate created another Issue")
	}
}

func TestInvalidDelegationSequenceAndLinkedIssueKeepFailureVisible(t *testing.T) {
	h := hostedManagerHarness(t)
	id, delivery := h.say("Repair the PWA and prepare a separate report")
	turn := startHostedReview(t, h, "main", delivery)
	p := delegationPayload(id)
	p.ExpectedSourceRevision = rev(1)
	cmd := CanonicalOwnerIssueCommand{Action: "create"}
	bad := hostedEffect(t, h, mainAgent, turn, 0, protocol.Target{Type: "issue"}, nil, p, cmd)
	expectWorkCode(t, bad, protocol.CodeInvalidRequest)
	p.Plan.Title = "Prepare the separate report"
	p.Plan.Intent = "Produce the report"
	good := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, p, cmd)
	if good.Status != protocol.OpSucceeded {
		t.Fatal(good)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(good.Result, &issue); err != nil {
		t.Fatal(err)
	}
	h.associate(mainAgent, id, issue.IssueID)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := messagingMessage(t.Context(), tx.Q(), id)
		if err == nil && m.Processing != "needs_review" {
			t.Fatal("linked Issue masked failed delegation", m)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
