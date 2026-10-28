package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func expectCanonicalLegacyGuard(t *testing.T, op protocol.Operation) {
	t.Helper()
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	if !strings.Contains(op.Error.Message, "canonical controls") {
		t.Fatalf("different guard rejected request: %+v", op)
	}
}

func TestCanonicalLegacyDefinitionAndTaskGuardsRollback(t *testing.T) {
	for _, action := range []string{"update", "close", "link task", "create task", "choice create", "followup create", "context prepare", "open background"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			h.app.followupsEnabled = true
			p := canonicalOwnerIssueCreate{Plan: workPlan()}
			p.Plan.Project = "default"
			created := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
			if created.Status != protocol.OpSucceeded {
				t.Fatal(created)
			}
			var issue protocol.Issue
			if err := json.Unmarshal(created.Result, &issue); err != nil {
				t.Fatal(err)
			}
			_, task := h.createTask(owner, createTaskPayload{Title: "Independent existing task", Spec: testSpec})
			evidence := h.ownerEvidence()
			target := workTarget(issue.IssueID)
			var command Command
			var payload any
			principal := owner
			revision := issue.Revision
			switch action {
			case "update":
				command = UpdateIssue{}
				payload = protocol.UpdateWorkItemPayload{WorkItemPlan: issue.Plan}
			case "close":
				command = CloseIssue{}
				payload = protocol.CloseWorkItemPayload{Closure: protocol.WorkItemClosure{Kind: protocol.WorkItemClosureDeclined, Reason: "Owner ends this responsibility", Evidence: []protocol.WorkItemEvidence{evidence}}}
			case "link task":
				command = LinkIssueTask{}
				payload = protocol.LinkWorkItemTaskPayload{TaskID: task.TaskID, StageIDs: []string{"investigate"}, SourceMessageID: evidence.Reference, CommitSeq: 1}
			case "create task":
				command = CreateTask{}
				target = taskTarget("")
				payload = createTaskPayload{Title: "Attempt canonical execution through legacy API", Spec: testSpec, WorkItemID: issue.IssueID, WorkItemRevision: issue.Revision, StageIDs: []string{"investigate"}}
			case "choice create":
				command = CreateWorkItemChoice{}
				payload = protocol.CreateWorkItemChoicePayload{Question: "Which outcome?", Options: []string{"Continue", "Later"}, SourceID: evidence.Reference}
			case "followup create":
				command = FollowupCommand{Action: "create"}
				payload = protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli(), Purpose: "Recheck current responsibility", Instruction: "Review existing facts", Timezone: "UTC"}
			case "open background":
				command = OpenAgentConversation{}
				payload = struct{}{}
			case "context prepare":
				command = ContextCommand{Action: "prepare"}
				payload = protocol.PrepareWorkItemContextPayload{Coverage: "Original owner source reviewed", Gaps: "No extra facts"}
				principal = mainAgent
				revision = h.agentContext(issue.IssueID).Revision
			}
			before := canonicalGuardBusinessSnapshot(t, h)
			op := h.exec(principal, h.env(command.Operation(), target, payload, rev(revision)), command)
			expectCanonicalLegacyGuard(t, op)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("legacy guard leaked business writes")
			}
		})
	}
}

func TestCanonicalLegacyChoiceControlsRejectFaultAssociatedRows(t *testing.T) {
	for _, action := range []string{"respond", "withdraw", "replace"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			legacy := h.createWork(workPlan())
			source := h.ownerEvidence().Reference
			choice := h.askChoice(legacy, source)
			// Exact fault fixture: a legacy choice points at a canonical ID. No
			// historical issue is adopted and this is not a product creation path.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE work_item_choices SET work_item_id=?,body_json=json_set(body_json,'$.work_item_id',?) WHERE choice_id=?`, issue.IssueID, issue.IssueID, choice.ChoiceID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			command := UpdateWorkItemChoice{Action: action}
			payload := protocol.UpdateWorkItemChoicePayload{Answer: "Continue", Replacement: &protocol.CreateWorkItemChoicePayload{Question: "Updated bounded choice?", Options: []string{"Continue", "Later"}, SourceID: source}}
			envelope := h.env(command.Operation(), protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}, payload, rev(choice.Version))
			envelope.Reason = "Owner corrects the question"
			before := canonicalGuardBusinessSnapshot(t, h)
			op := h.exec(owner, envelope, command)
			expectCanonicalLegacyGuard(t, op)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("choice guard leaked business writes")
			}
		})
	}
}

func TestCanonicalLegacyFollowupControlsRejectFaultAssociatedRows(t *testing.T) {
	for _, action := range []string{"update", "pause", "resume", "cancel", "trigger"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			legacy := h.createWork(workPlan())
			rule := h.followup(legacy, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli()})
			if action == "resume" {
				if op := h.controlFollowup(rule, "pause", nil); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				rule = h.readFollowup(rule.RuleID)
			}
			// Exact FK-valid fault association, not an enabled canonical rule
			// creator or a historical adoption/backfill.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE followup_rules SET work_item_id=?,body_json=json_set(body_json,'$.work_item_id',?) WHERE rule_id=?`, issue.IssueID, issue.IssueID, rule.RuleID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := canonicalGuardBusinessSnapshot(t, h)
			replacement := rule.FollowupSpec
			replacement.At = h.clock.Now().Add(2 * time.Hour).UnixMilli()
			op := h.controlFollowup(rule, action, &replacement)
			expectCanonicalLegacyGuard(t, op)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("followup guard leaked business writes")
			}
		})
	}
}

func TestCanonicalLegacyReopenRejectsStorageTerminalFixture(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	// Storage-only terminal fixture for an otherwise valid legacy reopen. This
	// does not claim a product close, achieved proof or dependency satisfaction.
	issue.State = protocol.IssueClosed
	issue.Revision++
	issue.Closure = &protocol.WorkItemClosure{Kind: protocol.WorkItemClosureDeclined, Reason: "Declined fixture endpoint"}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveCanonicalIssue(t.Context(), issue, issue.Revision-1, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest), Reason: "terminal storage guard fixture"})
	}); err != nil {
		t.Fatal(err)
	}
	before := canonicalGuardBusinessSnapshot(t, h)
	op := h.exec(owner, h.env("work_items.reopen", workTarget(issue.IssueID), protocol.ReopenWorkItemPayload{Reason: "Owner resumes responsibility"}, rev(issue.Revision)), ReopenIssue{})
	expectCanonicalLegacyGuard(t, op)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("legacy reopen escaped canonical controls")
	}
}
