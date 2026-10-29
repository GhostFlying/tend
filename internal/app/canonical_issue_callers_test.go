package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func canonicalRuleForTest(t *testing.T, op protocol.Operation) protocol.IssueFollowupRule {
	t.Helper()
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var r protocol.IssueFollowupRule
	if err := json.Unmarshal(op.Result, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func canonicalFollowupSpec(h *harness) protocol.FollowupSpec {
	return protocol.FollowupSpec{Purpose: "Recheck the agreed result", Instruction: "Read current facts, retain the approved scope", Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli(), Timezone: "UTC"}
}
func TestCanonicalFollowupOwnerLifecycleKeepsHistoryAndDeadlineAuthority(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	spec := canonicalFollowupSpec(h)
	payload := protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}
	cmd := IssueFollowupCommand{Action: "create"}
	env := h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	first := h.exec(owner, env, cmd)
	r := canonicalRuleForTest(t, first)
	if r.IssueID != issue.IssueID || r.SourceID != env.RequestID {
		t.Fatal("owner needed a second source transaction", r)
	}
	if replay := h.exec(owner, env, cmd); string(replay.Result) != string(first.Result) {
		t.Fatal(replay)
	}
	legacy := h.exec(owner, h.env("work_items.followups_create", workTarget(issue.IssueID), spec, rev(issue.Revision)), FollowupCommand{Action: "create"})
	if legacy.Error == nil || legacy.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("legacy guard removed", legacy)
	}
	for _, action := range []string{"pause", "resume", "update", "trigger", "cancel"} {
		p := protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}
		if action == "update" {
			spec.At += 3600000
			p.Spec = &spec
		}
		c := IssueFollowupCommand{Action: action}
		e := h.env(c.Operation(), protocol.Target{Type: "followup", ID: r.RuleID}, p, rev(r.Revision))
		e.Reason = "Owner updates the existing check"
		r = canonicalRuleForTest(t, h.exec(owner, e, c))
	}
	if r.Status != "cancelled" || r.TriggerCount != 1 || len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal(r, h.occurrences(r.RuleID))
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		history, err := views.CanonicalIssueFollowupHistory(t.Context(), tx.Q(), issue.IssueID, r.RuleID, "", 0)
		if err == nil && (len(history.Revisions) < 5 || len(history.Occurrences) != 1 || history.Rule.RuleID != r.RuleID) {
			t.Fatal(history)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 48*time.Hour))
	p := protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	e := h.env("issues.followups_pause", protocol.Target{Type: "followup", ID: d.NearRuleID}, p, rev(1))
	e.Reason = "Wrong generic entry"
	if denied := h.exec(owner, e, IssueFollowupCommand{Action: "pause"}); denied.Error == nil || denied.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("generic rewrote deadline window", denied)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.CanonicalIssueFollowups(t.Context(), tx.Q(), issue.IssueID, "", true)
		found := false
		for _, rule := range page.Items {
			if rule.RuleID == d.NearRuleID {
				found = rule.DeadlineID == d.DeadlineID
			}
		}
		if !found {
			t.Fatal("deadline controller not visible", page)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalManagerFollowupUsesOwnEpochAndWakesActualManager(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	spec := canonicalFollowupSpec(h)
	spec.At = h.clock.Now().Add(time.Second).UnixMilli()
	p := protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}
	r := canonicalRuleForTest(t, hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, IssueFollowupCommand{Action: "create"}))
	if r.SourceID != turn.MessageID {
		t.Fatal("lost actual source", r)
	}
	other := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	otherTurn, otherPrincipal := startHostedIssueReview(t, h, other)
	_, err := h.app.Execute(t.Context(), otherPrincipal, h.env("issues.followups_cancel", protocol.Target{Type: "followup", ID: r.RuleID}, protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch}, rev(r.Revision)), HostedCoordinationCommand{Command: IssueFollowupCommand{Action: "cancel"}, ExecutionID: otherTurn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cross-Issue rule write", err)
	}
	h.clock.Advance(2 * time.Second)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("generic rule did not fire")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) JOIN conversation_messages m ON m.message_id=b.message_id WHERE s.kind='followup.triggered' AND b.work_item_id=? AND m.conversation_id=?`, issue.IssueID, "issue:"+issue.IssueID).Scan(&n)
		if err == nil && n != 1 {
			t.Fatal("check did not reach actual Issue manager", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalDeliveryRefreshPreservesRunnerFactsAndRequest(t *testing.T) {
	h := newHarness(t)
	payload := protocol.CreateIssuePayload{Plan: protocol.WorkItemPlan{Title: "Track the agreed PR", Project: "default", Intent: "Observe the publication", Deliveries: []protocol.WorkItemDelivery{{DeliveryID: "delivery-1", Provider: "github", Repository: "example/project", Number: 7}}}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	observerHello(t, h, "linux-1", "observer")
	as := pollDelivery(t, h, "linux-1", "observer")
	if len(as) != 1 || !accepted(t, reportDelivery(t, h, "linux-1", "observer", as[0], "open", "")) {
		t.Fatal(as)
	}
	p := protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, DeliveryID: "delivery-1"}
	env := h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
	first := h.exec(owner, env, IssueRefreshDelivery{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	if op := h.exec(owner, env, IssueRefreshDelivery{}); string(op.Result) != string(first.Result) {
		t.Fatal("refresh replay changed generation", op)
	}
	queries := pollDelivery(t, h, "linux-1", "observer")
	if len(queries) != 1 || queries[0].WorkItemID != issue.IssueID || queries[0].Generation <= as[0].Generation {
		t.Fatal(queries)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", queries[0], "merged", "")) {
		t.Fatal("original runner report rejected")
	}
	if accepted(t, reportDelivery(t, h, "linux-1", "observer", as[0], "closed", "")) {
		t.Fatal("old nonce replaced newer facts")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.CanonicalIssueDeliveries(t.Context(), tx.Q(), issue.IssueID)
		if err == nil && (len(page.Items) != 1 || page.Items[0].Observation.State != "merged" || page.Items[0].Observation.CheckedAt == 0 || page.Items[0].Observation.RefreshPending) {
			t.Fatal(page)
		}
		current, err2 := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err2 != nil {
			return err2
		}
		if current.Revision != issue.Revision {
			t.Fatal("refresh changed definition")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalFollowupConditionsUseNoStagePrimaryTask(t *testing.T) {
	for _, kind := range []string{"tasks_completed", "decision_answered"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.app.followupsEnabled = true
			issue := createCanonicalOwnerTestIssue(t, h)
			task := createCanonicalTaskForTest(t, h, issue)
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			if len(issue.Plan.Stages) != 0 || h.count("work_item_task_links") != 0 {
				t.Fatal("fixture invented a stage/link")
			}
			condition := protocol.FollowupCondition{Kind: kind, TaskIDs: []string{task}}
			var attention protocol.Attention
			if kind == "decision_answered" {
				attention = attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, TaskID: task, Kind: "decision", Title: "Continue within current scope?", Options: []string{"continue"}})
				condition.TaskIDs = nil
				condition.DecisionID = attention.DecisionID
			}
			spec := protocol.FollowupSpec{Purpose: "Review the actual primary task", Instruction: "Read its original result", Kind: "condition", Timezone: "UTC", Condition: &condition}
			create := func(issue protocol.Issue, spec protocol.FollowupSpec) protocol.Operation {
				return h.exec(owner, h.env("issues.followups_create", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}, rev(issue.Revision)), IssueFollowupCommand{Action: "create"})
			}
			rule := canonicalRuleForTest(t, create(issue, spec))
			if len(h.occurrences(rule.RuleID)) != 0 {
				t.Fatal("condition satisfied before its fact")
			}
			if kind == "tasks_completed" {
				implementation, ok, _ := h.claim("linux-1")
				if !ok || implementation.TaskID != task {
					t.Fatal(implementation)
				}
				if op := h.runAs(implementation, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.runAs(implementation, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				acceptance, ok, _ := h.claim("linux-1")
				if !ok || acceptance.TaskID != task || acceptance.Role != protocol.RoleAccept {
					t.Fatal(acceptance)
				}
				if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				op := h.exec(owner, h.env("attentions.respond", protocol.Target{Type: "attention", ID: attention.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: "continue"}, rev(attention.Revision)), AttentionCommand{Action: "respond"})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if len(h.occurrences(rule.RuleID)) != 1 {
				t.Fatal("actual completion/answer did not trigger the original rule", h.occurrences(rule.RuleID))
			}
			other := createCanonicalOwnerTestIssue(t, h)
			otherTask := createCanonicalTaskForTest(t, h, other)
			otherCondition := protocol.FollowupCondition{Kind: kind, TaskIDs: []string{otherTask}}
			if kind == "decision_answered" {
				a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: other.IssueID, TaskID: otherTask, Kind: "decision", Title: "Other Issue question", Options: []string{"yes"}})
				otherCondition.TaskIDs = nil
				otherCondition.DecisionID = a.DecisionID
			}
			spec.Condition = &otherCondition
			denied := create(canonicalTaskIssue(t, h, issue.IssueID), spec)
			if denied.Error == nil || denied.Error.Code != protocol.CodeInvalidRequest {
				t.Fatal("foreign primary task admitted", denied)
			}
			if h.count("work_item_task_links") != 0 {
				t.Fatal("followup created a legacy membership")
			}
		})
	}
}
