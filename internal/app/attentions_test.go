package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"strings"
	"testing"
	"time"
)

func attentionForTest(t *testing.T, h *harness, p protocol.CreateAttentionPayload) protocol.Attention {
	t.Helper()
	op := h.exec(owner, h.env("attentions.create", protocol.Target{Type: "attention"}, p, nil), AttentionCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var a protocol.Attention
	if err := json.Unmarshal(op.Result, &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func readAttentionForTest(t *testing.T, h *harness, id string) protocol.Attention {
	t.Helper()
	var a protocol.Attention
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		a, err = store.GetAttention(t.Context(), tx.Q(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestAttentionMainRelayWakesManagerWithoutReassignment(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	first := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Choose bounded scope", Options: []string{"report", "implement"}, BlocksClose: true})
	second := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "A separate question", Options: []string{"yes", "no"}})
	if first.AttentionID == second.AttentionID || first.DecisionID == second.DecisionID {
		t.Fatal("independent questions merged")
	}
	source, delivery := h.say("Please select report only")
	turn := startHostedReview(t, h, "main", delivery)
	var before int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE destination='main_thread'`).Scan(&before)
	}); err != nil {
		t.Fatal(err)
	}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: first.AttentionID}, rev(first.Revision), protocol.RespondAttentionPayload{Version: 1, Option: "report", SourceMessageID: source}, AttentionCommand{Action: "respond", FromMessage: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if readAttentionForTest(t, h, first.AttentionID).Status != "resolved" || readAttentionForTest(t, h, second.AttentionID).Status != "open" {
		t.Fatal("answers were copied or lost")
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	if current.Assignee != issue.Assignee || current.AssignmentEpoch != issue.AssignmentEpoch {
		t.Fatal("question changed responsibility")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var answers, after int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE b.work_item_id=? AND s.kind='decision.answered'`, issue.IssueID).Scan(&answers); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE destination='main_thread'`).Scan(&after); err != nil {
			return err
		}
		if answers != 1 || after != before {
			t.Fatalf("manager relay lost or self main woke: answers%d main%d→%d", answers, before, after)
		}
		d, err := store.GetDecision(t.Context(), tx.Q(), first.DecisionID)
		if err == nil && d.Status != store.DecisionAnswered {
			t.Fatal(d)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestAttentionHumanActionsAndPresentationHaveSeparateMeaning(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Complete the owner action", BlocksClose: true})
	op := h.exec(owner, h.env("attentions.present", protocol.Target{Type: "attention", ID: a.AttentionID}, map[string]bool{"read": true, "archived": true}, rev(a.Revision)), AttentionCommand{Action: "present"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Status != "open" || a.ReadAt == 0 || a.ArchivedAt == 0 {
		t.Fatal(a)
	}
	close := canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted))
	if close.Status != protocol.OpFailed {
		t.Fatal("archive answered the human action", close)
	}
	confirm := h.env("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "I completed it"}, rev(a.Revision))
	op = h.exec(owner, confirm, AttentionCommand{Action: "confirm"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if replay := h.exec(owner, confirm, AttentionCommand{Action: "confirm"}); replay.Status != protocol.OpSucceeded || string(replay.Result) != string(op.Result) {
		t.Fatal("owner confirmation replay changed", replay)
	}
	info := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "information", Title: "Read this when useful"})
	if info.BlocksClose {
		t.Fatal(info)
	}
	if closed := canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)); closed.Status != protocol.OpSucceeded {
		t.Fatal("information created an acknowledgement requirement", closed)
	}
}
func TestAttentionWorkerAnswerKeepsOriginalRunAndLease(t *testing.T) {
	h := hostedManagerHarness(t)
	runtime := testRuntime("attention-worker")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	task := createCanonicalTaskForTest(t, h, issue)
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task || !as.WorkerQuestions {
		t.Fatal(as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	question := askQuestion(t, h, as, scopeQuestion)
	var id string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE decision_id=?`, question.DecisionID).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	a := readAttentionForTest(t, h, id)
	if a.IssueID != issue.IssueID || a.TaskID != task {
		t.Fatal(a)
	}
	source, delivery := h.say("Keep renderer only")
	turn := startHostedReview(t, h, "main", delivery)
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), protocol.RespondAttentionPayload{Version: question.Version, Option: scopeQuestion.Options[0], SourceMessageID: source}, AttentionCommand{Action: "respond", FromMessage: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	received, err := h.app.WorkerQuestion(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, question.DecisionID)
	if err != nil || received.Status != store.DecisionAnswered || received.RunID != as.RunID || h.run(as.RunID).ObservedState != protocol.RunRunning || h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("answer replaced or released worker", received, err)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("same worker could not continue", op)
	}
	acceptance, ok := h.claimInputRun(runtime)
	if !ok || acceptance.TaskID != task || acceptance.Role != protocol.RoleAccept || acceptance.RunID == as.RunID {
		t.Fatal("answered worker result lost independent acceptance", acceptance, ok)
	}
	if strings.Contains(string(acceptance.Spec), "PRIVATE_OWNER_IMPLEMENTATION_REASONING") {
		t.Fatal("implementation rationale leaked to acceptance")
	}
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).Lifecycle != protocol.TaskCompleted {
		t.Fatal("formal acceptance did not complete original task", h.task(task))
	}
}

func TestAttentionPendingAnswerFollowsHumanResponsibilityHandoff(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Choose scope", Options: []string{"report"}})
	op := h.exec(owner, h.env("attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: "report"}, rev(a.Revision)), AttentionCommand{Action: "respond"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignment := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "The human owner takes responsibility"}
	fenced := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, assignment, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: fenced.AssignmentEpoch, HandoffID: fenced.PendingHandoffID, Reason: "No manager turn was admitted"}
	current := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(fenced.Revision)), HostedIssueAssignment{Finish: true}))
	if current.Assignee.Kind != protocol.IssueAssigneeOwner {
		t.Fatal(current)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var retained, pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE b.group_key=? AND b.work_item_id='' AND s.kind='decision.answered'`, "owner-issue:"+issue.IssueID).Scan(&retained); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_batches WHERE work_item_id=? AND status='pending'`, issue.IssueID).Scan(&pending); err != nil {
			return err
		}
		if retained != 1 || pending != 0 {
			t.Fatal("unclaimed answer stranded on the old manager", retained, pending)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionStableQuestionRevisionAndWithdrawalDoNotInventAnswer(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Clarify the delivery", Options: []string{"report"}})
	p := protocol.UpdateAttentionPayload{Title: "Clarify the delivery format", Options: []string{"written report", "demo"}, DecisionVersion: 1, BlocksClose: true}
	op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(a.Revision)), AttentionCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := readAttentionForTest(t, h, a.AttentionID)
	if current.AttentionID != a.AttentionID || current.DecisionID != a.DecisionID || current.QuestionVersion != 2 {
		t.Fatal(current)
	}
	stale := h.exec(owner, h.env("attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: "report"}, rev(current.Revision)), AttentionCommand{Action: "respond"})
	if stale.Status != protocol.OpFailed {
		t.Fatal("old question answered the new version", stale)
	}
	env := h.env("attentions.withdraw", protocol.Target{Type: "attention", ID: a.AttentionID}, struct{}{}, rev(current.Revision))
	env.Reason = "The owner no longer needs this clarification"
	if op := h.exec(owner, env, AttentionCommand{Action: "withdraw"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		d, err := store.GetDecision(t.Context(), tx.Q(), a.DecisionID)
		if err == nil && (d.Status != "cancelled" || d.AnswerJSON != "") {
			t.Fatal("withdrawal invented an answer", d)
		}
		var raw string
		if err == nil {
			err = tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM attention_revisions WHERE attention_id=? AND revision=1`, a.AttentionID).Scan(&raw)
		}
		if err == nil {
			var original protocol.Attention
			err = json.Unmarshal([]byte(raw), &original)
			if original.Title != a.Title || original.QuestionVersion != 1 || original.QuestionOptions[0] != "report" {
				t.Fatal("original question history lost", original)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionManagerRelayUsesActualLocalOwnerSource(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Choose report scope", Options: []string{"report"}})
	foreign := queueMessage(t, h, "Another responsibility's owner input")
	submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + issue.IssueID, Body: "I choose report"}, nil), SubmitMessage{})
	if submitted.Status != protocol.OpAccepted {
		t.Fatal(submitted)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	turn := startHostedReview(t, h, "issue:"+issue.IssueID, delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "actual-local-manager", Thread: turn.ThreadID, ConversationID: "issue:" + issue.IssueID}
	p := protocol.RespondAttentionPayload{Version: 1, Option: "report", SourceMessageID: foreign}
	env := h.env("attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(a.Revision))
	command := HostedCoordinationCommand{Command: AttentionCommand{Action: "respond", FromMessage: true}, ExecutionID: turn.RequestID, EffectSeq: 1}
	if _, err := h.app.Execute(t.Context(), principal, env, command); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("local manager consumed another source as authority", err)
	}
	p.SourceMessageID = turn.MessageID
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), p, AttentionCommand{Action: "respond", FromMessage: true}); op.Status != protocol.OpSucceeded {
		t.Fatal("real direct owner answer required main approval", op)
	}
}

func TestFault_AttentionQuestionAndHistoryRollBackTogether(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_attention_history BEFORE INSERT ON attention_revisions BEGIN SELECT RAISE(ABORT,'injected history failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	due := h.clock.Now().Add(30 * time.Hour).UnixMilli()
	env := h.env("attentions.create", protocol.Target{Type: "attention"}, protocol.CreateAttentionPayload{Kind: "decision", Title: "A transactional question", Options: []string{"yes"}, LatestActionAt: &due}, nil)
	if _, err := h.app.Execute(t.Context(), owner, env, AttentionCommand{Action: "create"}); err == nil {
		t.Fatal("injected failure ignored")
	}
	if h.count("attentions") != 0 || h.count("decisions") != 0 || h.count("attention_revisions") != 0 || h.count("followup_rules") != 0 || h.count("followup_revisions") != 0 || h.count("followup_targets") != 0 {
		t.Fatal("partial original question escaped rollback")
	}
}
