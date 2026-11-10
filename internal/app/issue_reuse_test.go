package app

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func reusePayload(t *testing.T, h *harness, issue protocol.Issue, source string) protocol.ReuseIssuePayload {
	t.Helper()
	status, err := h.app.IssueReuseStatus(t.Context(), issue.IssueID)
	if err != nil || status.OriginObservation != "recorded" {
		t.Fatal(status, err)
	}
	var associations protocol.MessageAssociations
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		associations, err = store.MessageAssociations(t.Context(), tx.Q(), source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return protocol.ReuseIssuePayload{IssueControlPayload: protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: rev(associations.ContentRevision)}, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Continue this exact original responsibility from the new owner clarification"}, OriginalRequestID: status.OriginalRequestID, ExpectedAssociationRevision: rev(associations.Revision)}
}
func TestIssueReuseDistinctRequestsPreserveResponsibilityAndReplay(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	acceptDeliveryTaskForTest(t, h, task)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	oldPlan, _ := json.Marshal(issue.Plan)
	original, err := h.app.IssueReuseStatus(t.Context(), issue.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	var originalReceipt store.RequestRecord
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		originalReceipt, err = store.GetRequest(t.Context(), tx.Q(), owner.DedupDomain(), original.OriginalRequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	obligations := h.count("issue_delivery_duties")
	var first protocol.Operation
	var firstEnv *protocol.Envelope
	for n := 0; n < 2; n++ {
		source := queueMessage(t, h, "An additional owner input for the same original responsibility")
		p := reusePayload(t, h, issue, source)
		env := h.env("issues.reuse", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
		op := h.exec(owner, env, ReuseIssue{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var result protocol.IssueReuseResult
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Issue.IssueID != issue.IssueID || result.Issue.AssignmentEpoch != issue.AssignmentEpoch || result.Issue.Assignee != issue.Assignee || result.Issue.Revision != issue.Revision+1 || !result.Reused {
			t.Fatal(result)
		}
		plan, _ := json.Marshal(result.Issue.Plan)
		if !bytes.Equal(plan, oldPlan) {
			t.Fatal("reuse changed original plan")
		}
		var needsReview bool
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, source, issue.IssueID).Scan(&needsReview)
		}); err != nil || !needsReview {
			t.Fatal("new source lacks its real review obligation", err)
		}
		if result.Source.FactsReviewed {
			t.Fatal("new source was falsely reviewed", result.Source)
		}
		if n == 0 {
			first = op
			firstEnv = env
		}
		issue = result.Issue
	}
	if h.count("issue_controls") != 1 || h.task(task).Lifecycle != protocol.TaskCompleted || h.count("issue_delivery_duties") != obligations {
		t.Fatal("reuse recreated responsibility or rewrote terminal/delivery facts")
	}
	events := h.count("events")
	replay := h.exec(owner, firstEnv, ReuseIssue{})
	if !bytes.Equal(first.Result, replay.Result) || events != h.count("events") {
		t.Fatal("same request replay mutated later responsibility")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetRequest(t.Context(), tx.Q(), owner.DedupDomain(), original.OriginalRequestID)
		if err == nil && r.ResultJSON != originalReceipt.ResultJSON {
			t.Fatal("original creation receipt changed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestIssueReuseRejectsIdentityDriftWithoutPartialAssociation(t *testing.T) {
	for _, kind := range []string{"origin", "issue revision", "epoch", "source revision", "association revision", "missing association revision", "missing source revision", "withdrawn", "unknown origin", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			source := queueMessage(t, h, "Use the existing obligation")
			p := reusePayload(t, h, issue, source)
			expected := issue.Revision
			switch kind {
			case "origin":
				p.OriginalRequestID = h.ids.New(protocol.PrefixRequest)
			case "issue revision":
				expected++
			case "epoch":
				p.ExpectedAssignmentEpoch++
			case "source revision":
				p.ExpectedSourceRevision = rev(2)
			case "association revision":
				p.ExpectedAssociationRevision = rev(*p.ExpectedAssociationRevision + 1)
			case "missing association revision":
				p.ExpectedAssociationRevision = nil
			case "missing source revision":
				p.ExpectedSourceRevision = nil
			case "withdrawn":
				var c protocol.MessageControl
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					c, err = store.MessageControl(t.Context(), tx.Q(), source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: source}, protocol.WithdrawMessagePayload{}, rev(c.Revision)), WithdrawMessage{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "unknown origin":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `DELETE FROM request_records WHERE request_id=?`, p.OriginalRequestID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "terminal":
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)))
				expected = issue.Revision
			}
			before := canonicalGuardBusinessSnapshot(t, h)
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.reuse", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(expected)), ReuseIssue{})
			if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("invalid reuse succeeded", kind)
			}
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("rejected reuse changed business state", kind)
			}
		})
	}
}
func TestIssueReuseMainRequiresCurrentExecutionSource(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "historical"}[historical], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			source, delivery := h.say("Reuse this existing responsibility")
			turn := startHostedReview(t, h, "main", delivery)
			if historical {
				source = queueMessage(t, h, "Unadmitted source from another turn")
			}
			p := reusePayload(t, h, issue, source)
			command := HostedCoordinationCommand{Command: ReuseIssue{}, ExecutionID: turn.RequestID, EffectSeq: 1}
			op, err := h.app.Execute(t.Context(), mainAgent, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), command)
			if !historical && (err != nil || op.Status != protocol.OpSucceeded) {
				t.Fatal(op, err)
			}
			if historical && err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("noncurrent source authorized reuse")
			}
		})
	}
}

func TestIssueReuseRejectsPreviouslyAdmittedSourceInNewTurn(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	source, delivery := h.say("Previous input")
	previous := startHostedReview(t, h, "main", delivery)
	finishHostedReview(t, h, previous)
	_, nextDelivery := h.say("Inspect only, no new reuse instruction")
	current := startHostedReview(t, h, "main", nextDelivery)
	var admitted bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		admitted, err = store.OwnerInputAdmitted(t.Context(), tx.Q(), source)
		return err
	}); err != nil || !admitted {
		t.Fatal("fixture source was not previously admitted", err)
	}
	p := reusePayload(t, h, issue, source)
	cmd := HostedCoordinationCommand{Command: ReuseIssue{}, ExecutionID: current.RequestID, EffectSeq: 1}
	before := canonicalGuardBusinessSnapshot(t, h)
	op, err := h.app.Execute(t.Context(), mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), cmd)
	if err == nil && op.Status == protocol.OpSucceeded {
		t.Fatal("historical owner source authorized new reuse")
	}
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("historical source changed current responsibility")
	}
}

func TestIssueReuseFromActualMainCreationReceiptInLaterTurn(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Create this specific responsibility")
	turn := startHostedReview(t, h, "main", delivery)
	payload := delegationPayload(source)
	payload.ExpectedSourceRevision = rev(1)
	command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1}
	original := h.env(command.Operation(), protocol.Target{Type: "issue"}, payload, nil)
	created := h.exec(mainAgent, original, command)
	issue := canonicalLifecycleIssue(t, created)
	status, err := h.app.IssueReuseStatus(t.Context(), issue.IssueID)
	if err != nil || status.OriginObservation != "recorded" || status.OriginalRequestID != original.RequestID {
		t.Fatal("actual Main creation provenance unavailable", status, err)
	}
	finishHostedReview(t, h, turn)
	nextSource, nextDelivery := h.say("Continue that exact original obligation with this new detail")
	current := startHostedReview(t, h, "main", nextDelivery)
	p := reusePayload(t, h, issue, nextSource)
	reuse := HostedCoordinationCommand{Command: ReuseIssue{}, ExecutionID: current.RequestID, EffectSeq: 1}
	env := h.env(reuse.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
	op := h.exec(mainAgent, env, reuse)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result protocol.IssueReuseResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Issue.IssueID != issue.IssueID || result.Issue.AssignmentEpoch != issue.AssignmentEpoch || result.OriginalRequestID != original.RequestID || h.count("issue_controls") != 1 {
		t.Fatal(result)
	}
	replay := h.exec(mainAgent, original, command)
	if !bytes.Equal(replay.Result, created.Result) {
		t.Fatal("reuse replaced original Main creation receipt")
	}
	finishHostedReview(t, h, current)
	replayReuse := h.exec(mainAgent, env, reuse)
	if replayReuse.Status != protocol.OpSucceeded || !bytes.Equal(replayReuse.Result, op.Result) {
		t.Fatal("settled native execution lost its original reuse receipt", replayReuse)
	}
}

func TestIssueReuseHistoricalClassificationDoesNotAdmitAuthority(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	source, delivery := h.say("Previous owner input")
	previous := startHostedReview(t, h, "main", delivery)
	finishHostedReview(t, h, previous)
	_, nextDelivery := h.say("Inspect only; no reuse authorization")
	current := startHostedReview(t, h, "main", nextDelivery)
	var before, after bool
	query := `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?)`
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), query, current.RequestID, source).Scan(&before)
	}); err != nil {
		t.Fatal(err)
	}
	a := h.associations(source)
	classify := hostedEffect(t, h, mainAgent, current, 1, protocol.Target{Type: "message", ID: source}, rev(a.Revision), protocol.SetIssueMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: a.ContentRevision}, SetIssueMessageAssociations{})
	if classify.Status != protocol.OpSucceeded {
		t.Fatalf("classification failed: %+v", classify)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), query, current.RequestID, source).Scan(&after)
	}); err != nil {
		t.Fatal(err)
	}
	p := reusePayload(t, h, issue, source)
	cmd := HostedCoordinationCommand{Command: ReuseIssue{}, ExecutionID: current.RequestID, EffectSeq: 2}
	op, err := h.app.Execute(t.Context(), mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), cmd)
	t.Logf("current membership before=%v after-classification=%v; reuse status=%s err=%v", before, after, op.Status, err)
	if err == nil && op.Status == protocol.OpSucceeded {
		t.Fatal("historical source was accepted as fresh reuse authorization after display-only classification")
	}
}

func TestIssueReusePreservesLegacyAssociation(t *testing.T) {
	h := newHarness(t)
	legacy := h.createWork(workPlan())
	issue := createCanonicalOwnerTestIssue(t, h)
	source := queueMessage(t, h, "Additional owner input for both responsibilities")
	if op := h.associate(mainAgent, source, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.associations(source)
	p := reusePayload(t, h, issue, source)
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.reuse", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), ReuseIssue{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("could not preserve existing valid legacy association while adding reuse target: status=%s op-error=%v err=%v", op.Status, op.Error, err)
	}
	after := h.associations(source)
	if len(before.Items) != 1 || len(after.Items) != 2 {
		t.Fatal(before, after)
	}
	matched := false
	for _, item := range after.Items {
		if item.WorkItemID == legacy.WorkItemID {
			matched = true
			if item != before.Items[0] {
				t.Fatal("legacy display link changed", before.Items[0], item)
			}
		}
	}
	if !matched {
		t.Fatal("legacy link lost")
	}

}

func TestMainExecutionSourceIdentityRetainsNativeContinuationAndConsumedSteer(t *testing.T) {
	h, active := activeSteerEnv(t)
	var original store.MainTurn
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		original, err = store.GetMainTurn(t.Context(), tx.Q(), active.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check := func(request, source string, want bool) {
		t.Helper()
		var got bool
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			got, err = mainExecutionSourceAdmitted(t.Context(), tx.Q(), request, source)
			return err
		})
		if err != nil || got != want {
			t.Fatalf("source %s admitted=%t want=%t err=%v", source, got, want, err)
		}
	}
	check(active.RequestID, original.MessageID, true)
	source := queueMessage(t, h, "A supplement requiring actual native consumption")
	check(active.RequestID, source, false)
	steer, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	row, found, err := h.app.MainSteer(t.Context(), steer.RequestID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	row.InputText = "exact additional input"
	row.Status = "accepted"
	if err := h.app.UpdateMainSteer(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	check(active.RequestID, source, false)
	if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "native", "turn", row.InputText, "actual-native-item"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	check(active.RequestID, source, true)
	if err := h.app.FinishDelivery(t.Context(), steer, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	original.Status = store.TurnFailed
	original.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), active, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	repair, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || repair.Operation != OpRecoverAgent {
		t.Fatal(repair, ok, err)
	}
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), repair.RequestID, original.ThreadID); err != nil || !admitted {
		t.Fatal(admitted, err)
	}
	continued := original
	continued.RequestID, continued.TurnID = repair.RequestID, "actual-native-continuation"
	continued.Status, continued.Failure = store.TurnStarted, nil
	if err := h.app.PutMainTurn(t.Context(), continued); err != nil {
		t.Fatal(err)
	}
	check(repair.RequestID, original.MessageID, true)
	check(repair.RequestID, source, true)
	other := queueMessage(t, h, "A queued unrelated owner input")
	check(repair.RequestID, other, false)
}

func TestMainExecutionSourceIdentityRetainsEmptyContinuation(t *testing.T) {
	h := newHarness(t)
	original := failedRecoveryTurn(t, h)
	h.clock.Advance(time.Minute)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || delivery.Operation != OpRecoverAgent {
		t.Fatal(delivery, ok, err)
	}
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), delivery.RequestID, original.ThreadID); err != nil || !admitted {
		t.Fatal(admitted, err)
	}
	continued := original
	continued.RequestID = delivery.RequestID
	continued.TurnID = "actual-empty-continuation"
	continued.Status = store.TurnStarted
	continued.Failure = nil
	if err := h.app.PutMainTurn(t.Context(), continued); err != nil {
		t.Fatal(err)
	}
	unrelated := queueMessage(t, h, "A queued message is not input to the empty continuation")
	for _, c := range []struct {
		source string
		want   bool
	}{{original.MessageID, true}, {unrelated, false}} {
		var got bool
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			got, err = mainExecutionSourceAdmitted(t.Context(), tx.Q(), continued.RequestID, c.source)
			return err
		})
		if err != nil || got != c.want {
			t.Fatal(c, got, err)
		}
	}
}

func TestMainExecutionSourceIdentityClassificationCannotAuthorizeCreateOrReopen(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)))
	source, delivery := h.say("An old owner instruction")
	previous := startHostedReview(t, h, "main", delivery)
	finishHostedReview(t, h, previous)
	_, next := h.say("Inspect existing state only")
	current := startHostedReview(t, h, "main", next)
	assoc := h.associations(source)
	classify := hostedEffect(t, h, mainAgent, current, 1, protocol.Target{Type: "message", ID: source}, rev(assoc.Revision), protocol.SetIssueMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: assoc.ContentRevision}, SetIssueMessageAssociations{})
	if classify.Status != protocol.OpSucceeded {
		t.Fatal(classify)
	}
	create := delegationPayload(source)
	create.ExpectedSourceRevision = rev(1)
	cases := []struct {
		command  Command
		target   protocol.Target
		revision *int64
		payload  any
	}{
		{CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, nil, create},
		{CanonicalOwnerIssueCommand{Action: "reopen"}, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(closed.Revision), protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: rev(1)}, ExpectedAssignmentEpoch: closed.AssignmentEpoch}},
	}
	for n, c := range cases {
		issueCount, taskCount := h.count("issue_controls"), h.count("tasks")
		cmd := HostedCoordinationCommand{Command: c.command, ExecutionID: current.RequestID, EffectSeq: int64(n + 2)}
		op, err := h.app.Execute(t.Context(), mainAgent, h.env(cmd.Operation(), c.target, c.payload, c.revision), cmd)
		if protocol.CodeOf(err) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
			t.Fatal("classified historical source reached control command", cmd.Operation(), op, err)
		}
		after := canonicalTaskIssue(t, h, issue.IssueID)
		if h.count("issue_controls") != issueCount || h.count("tasks") != taskCount || after.Revision != closed.Revision || after.State != closed.State {
			t.Fatal("source gate created or reopened responsibility", cmd.Operation())
		}
		// The existing failed-delegation audit/notice remains a truthful failure fact.

	}
}

func TestMainExecutionSourceIdentityAdoptedReviewIsNotNativeAdmission(t *testing.T) {
	h := newHarness(t)
	source, original := startReviewTurn(t, h)
	await := AwaitTurnClarification{RequestID: original.RequestID}
	if op := h.exec(mainAgent, h.env(await.Operation(), protocol.Target{Type: "logical_turn", ID: original.RequestID}, protocol.TurnClarification{Question: "Which original responsibility do you mean?"}, nil), await); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, original)
	currentSource, delivery := h.say("Inspect the retained review state")
	current := store.MainTurn{RequestID: delivery.RequestID, MessageID: currentSource, ThreadID: mainAgent.Thread, TurnID: "current-native", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), current.RequestID)
	if err != nil || len(state.Awaiting) != 1 {
		t.Fatal(state, err)
	}
	adopt := AdoptClarification{RequestID: current.RequestID}
	if op := h.exec(mainAgent, h.env(adopt.Operation(), protocol.Target{Type: "logical_turn", ID: original.RequestID}, struct{}{}, rev(state.Awaiting[0].Revision)), adopt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var member bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_messages m JOIN logical_turn_executions e USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?)`, current.RequestID, source).Scan(&member); err != nil {
			return err
		}
		if !member {
			t.Fatal("actual adopted responsibility missing")
		}
		old, err := mainExecutionSourceAdmitted(t.Context(), tx.Q(), current.RequestID, source)
		if err != nil {
			return err
		}
		fresh, err := mainExecutionSourceAdmitted(t.Context(), tx.Q(), current.RequestID, currentSource)
		if err == nil && (old || !fresh) {
			t.Fatal("adoption manufactured native admission", old, fresh)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueReuseRecognizesSuccessfulConsultationCreation(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	source, _, _ := consultationTurn(t, h, c, "Please investigate this goal")
	payload := protocol.PromoteConsultationPayload{
		IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: rev(1)},
		Plan:                 protocol.IssuePlan{Title: "Original responsibility", Project: "default", Intent: "Investigate"},
		Assignee:             protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"},
	}
	create := h.env("conversation.promote", protocol.Target{Type: "conversation", ID: c.ID}, payload, nil)
	created := h.exec(owner, create, PromoteConsultation{})
	issue := canonicalLifecycleIssue(t, created)
	var original store.RequestRecord
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		original, err = store.GetRequest(t.Context(), tx.Q(), owner.DedupDomain(), create.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, err := h.app.IssueReuseStatus(t.Context(), issue.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt protocol.Issue
	if err := json.Unmarshal([]byte(original.ResultJSON), &receipt); err != nil {
		t.Fatal(err)
	}
	t.Logf("creation request=%s status=%s receipt issue=%s revision=%d; reuse origin=%s", original.Operation, original.Status, receipt.IssueID, receipt.Revision, status.OriginObservation)
	next := queueMessage(t, h, "Continue the same original responsibility")
	assoc := h.associations(next)
	reuse := protocol.ReuseIssuePayload{
		IssueControlPayload: protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: next, ExpectedSourceRevision: rev(assoc.ContentRevision)}, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Explicitly continue original responsibility"},
		OriginalRequestID:   create.RequestID, ExpectedAssociationRevision: rev(assoc.Revision),
	}
	reuseRequest := h.env("issues.reuse", protocol.Target{Type: "issue", ID: issue.IssueID}, reuse, rev(issue.Revision))
	op, err := h.app.Execute(t.Context(), owner, reuseRequest, ReuseIssue{})
	t.Logf("reuse operation status=%s error=%v", op.Status, err)
	if status.OriginObservation != "recorded" || err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("confirmed consultation-created Issue cannot be reused")
	}
	replayed, err := h.app.Execute(t.Context(), owner, reuseRequest, ReuseIssue{})
	if err != nil || replayed.Status != op.Status || !bytes.Equal(replayed.Result, op.Result) {
		t.Fatal("promotion-based reuse did not replay its exact result", replayed, err)
	}
	for _, invalid := range []struct {
		name, operation, result string
		status                  protocol.OpStatus
	}{
		{"other operation", "conversation.associate_issues", original.ResultJSON, protocol.OpSucceeded},
		{"unknown", original.Operation, original.ResultJSON, protocol.OpUnknown},
		{"failed", original.Operation, original.ResultJSON, protocol.OpFailed},
		{"other issue", original.Operation, `{"issue_id":"other","revision":1}`, protocol.OpSucceeded},
		{"later revision", original.Operation, `{"issue_id":"` + issue.IssueID + `","revision":2}`, protocol.OpSucceeded},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE request_records SET operation=?,status=?,result_json=? WHERE domain=? AND request_id=?`, invalid.operation, invalid.status, invalid.result, owner.DedupDomain(), create.RequestID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			got, err := h.app.IssueReuseStatus(t.Context(), issue.IssueID)
			if err != nil || got.OriginObservation != "observation_unavailable" || got.OriginalRequestID != "" {
				t.Fatal("unconfirmed/mismatched promotion became an origin", got, err)
			}
		})
	}

}
