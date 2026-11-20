package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestReportFollowOnDecision(t *testing.T) {
	for _, tc := range []struct {
		option     string
		beforeStop bool
	}{
		{option: protocol.ReportContinueImplementation},
		{option: protocol.ReportContinueImplementation, beforeStop: true},
		{option: protocol.ReportMoveToBacklog, beforeStop: true},
		{option: protocol.ReportCloseOnly},
		{option: protocol.ReportCloseOnly, beforeStop: true},
	} {
		name := tc.option
		if tc.beforeStop {
			name += "/before-stop"
		}
		t.Run(name, func(t *testing.T) {
			h, runtime, issue, run, _ := reviewManagerAssignment(t)
			target := protocol.Target{Type: "issue", ID: issue.IssueID}
			issue.Plan.ReportFollowOnDecision = true
			op := h.exec(owner, h.env("issues.update", target, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			report := "Investigated the original layout. The report proposes one bounded improvement."
			upload, err := h.app.UploadDeliverableFile(t.Context(), runPrincipal(run.RunID, run.Role), h.ids.New(protocol.PrefixRequest), "findings.md", strings.NewReader(report))
			var file protocol.ArtifactFile
			if err != nil || upload.Status != protocol.OpSucceeded || json.Unmarshal(upload.Result, &file) != nil {
				t.Fatal(upload, err)
			}
			p := protocol.UpdateIssueDeliveryPayload{
				Action: "submit_result", TaskID: run.TaskID, RunID: run.RunID,
				Result:              &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: report}, Files: []protocol.ArtifactFile{file}},
				IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Deliver the actual investigation"},
			}
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+run.Manager.ConversationID)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			turn := startHostedReview(t, h, run.Manager.ConversationID, delivery)
			producer := runPrincipal(run.RunID, run.Role)
			producer.Thread, producer.ConversationID = turn.ThreadID, run.Manager.ConversationID
			p.RunID = "" // The hosted callback derives the producing Run.
			op = hostedEffect(t, h, producer, turn, 1, target, rev(issue.Revision), p, UpdateIssueDelivery{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			artifactID := h.task(run.TaskID).CurrentArtifactID
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			if repeat := hostedEffect(t, h, producer, turn, 2, target, rev(issue.Revision), p, UpdateIssueDelivery{}); repeat.Status != protocol.OpSucceeded {
				t.Fatal("the original report could not be recovered", repeat)
			}
			p.Action = "report"
			p.Result = nil
			p.ProposedImplementationScope = "Fix the layout of the original results panel."
			op = hostedEffect(t, h, producer, turn, 3, target, rev(issue.Revision), p, UpdateIssueDelivery{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// Reconstruct the settlement after the original response might have
			// been lost. Both the original stale retry and a current read preserve
			// the same report and choice.
			// Recover via the existing owner CLI request surface as well.
			p.RunID = run.RunID
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			if op := h.exec(owner, h.env("issues.update_delivery", target, p, rev(issue.Revision)), UpdateIssueDelivery{}); op.Status != protocol.OpSucceeded {
				t.Fatal("settled report could not be recovered", op)
			}
			if h.count("artifacts") != 1 || h.count("attentions") != 1 || h.count("run_external_waits") != 1 {
				t.Fatal("report retry duplicated the report, decision or wait")
			}

			var item protocol.AttentionView
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{IssueID: issue.IssueID, Status: "open", Recipient: "owner", ActionableOnly: true})
				if err != nil {
					return err
				}
				if len(page.Items) != 1 {
					t.Fatalf("Inbox did not contain the actual owner decision: %+v", page.Items)
				}
				item = page.Items[0]
				board, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true, WaitingOnOwner: owner.Subject})
				if err != nil {
					return err
				}
				if len(board.Items) != 1 || board.Items[0].IssueID != issue.IssueID {
					t.Fatalf("waiting-on-me filter did not include the original Issue: %+v", board.Items)
				}
				if item.ReportFollowOn == nil || item.ReportFollowOn.ArtifactID != artifactID ||
					item.ReportFollowOn.ImplementationScope != p.ProposedImplementationScope || !item.BlocksClose ||
					!slices.Contains(item.QuestionOptions, tc.option) {
					t.Fatalf("Inbox did not retain the exact report and decision: %+v", item)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			close := protocol.IssueEndpointPayload{IssueControlPayload: p.IssueControlPayload, Closure: &protocol.IssueClosure{
				Kind: protocol.WorkItemClosureCompleted, Reason: "Premature closure",
				Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Endpoint request"}},
			}}
			expectWorkCode(t, h.exec(owner, h.env("issues.close", target, close, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "close"}), protocol.CodeInvalidTransition)
			answer := func() {
				t.Helper()
				aTarget := protocol.Target{Type: "attention", ID: item.AttentionID}
				mixed := protocol.RespondAttentionPayload{Version: item.QuestionVersion, Option: tc.option, Text: "Only if the scope changes"}
				expectWorkCode(t, h.exec(owner, h.env("attentions.respond", aTarget, mixed, rev(item.Revision)), AttentionCommand{Action: "respond"}), protocol.CodeInvalidRequest)
				payload := protocol.RespondAttentionPayload{Version: item.QuestionVersion, Option: tc.option}
				env := h.env("attentions.respond", aTarget, payload, rev(item.Revision))
				if op := h.exec(owner, env, AttentionCommand{Action: "respond"}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				_ = h.exec(owner, env, AttentionCommand{Action: "respond"})
				if h.count("events WHERE kind='decision.answered'") != 1 {
					t.Fatal("retry duplicated the authenticated answer")
				}
			}

			if tc.beforeStop {
				answer()
				expectWorkCode(t, h.exec(owner, h.env("issues.close", target, close, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "close"}), protocol.CodeInvalidTransition)
			}
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if h.lease(run.RunID).Status != store.LeaseActive || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 ||
				canonicalTaskIssue(t, h, issue.IssueID).BoardStatus() != issue.BoardStatus() {
				t.Fatal("decision advanced before the runner's original stop")
			}
			// The runner also reconciles the original hosted turn's receipt.
			finishHostedReview(t, h, turn)
			stopOwnerClaim(t, h, run)
			if h.lease(run.RunID).Status != store.LeaseReleased {
				t.Fatal("the original stop did not release the physical slot")
			}
			if !tc.beforeStop {
				h.clock.Advance(21 * time.Minute)
				if _, err := h.heartbeat(t.Context(), runner("linux-1"), nil); err != nil {
					t.Fatal(err)
				}
				if err := h.app.SweepExternalWaits(t.Context()); err != nil {
					t.Fatal(err)
				}
				waits, err := h.app.ExternalWaits(t.Context(), run.TaskID)
				if err != nil || len(waits) != 1 || waits[0].Status != "waiting" || !waits[0].Stopped || ownerClaim(t, h, runtime).RunID != "" {
					t.Fatal("unanswered report did not stay stopped and waiting", waits, err)
				}
				answer()
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			switch tc.option {
			case protocol.ReportContinueImplementation:
				next := ownerClaim(t, h, runtime)
				if next.TaskID != run.TaskID || next.RunID == run.RunID || next.Manager == nil || next.Manager.IssueID != issue.IssueID ||
					next.Execution == nil || run.Execution == nil || next.Execution.Identity != run.Execution.Identity ||
					next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.TargetID != item.DecisionID {
					wait, _ := h.app.ExternalWaits(t.Context(), run.TaskID)
					t.Fatalf("implementation was not handed to the original owner: previous %+v, next %+v, wait %+v, task %+v", run, next, wait, h.task(run.TaskID))
				}
				if h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 {
					t.Fatal("answer did not create exactly one continuation")
				}
			case protocol.ReportMoveToBacklog:
				if issue.BoardStatus() != protocol.IssueBacklog || ownerClaim(t, h, runtime).RunID != "" || issue.Closure != nil {
					t.Fatalf("Backlog did not end admission: %+v", issue)
				}
			case protocol.ReportCloseOnly:
				if issue.State != protocol.IssueClosed || issue.Closure == nil ||
					!slices.ContainsFunc(issue.Closure.Evidence, func(e protocol.IssueEvidence) bool { return e.Reference == artifactID }) ||
					!slices.ContainsFunc(issue.Closure.Evidence, func(e protocol.IssueEvidence) bool { return e.Reference == item.AttentionID }) {
					t.Fatalf("report-only did not complete with original report and answer evidence: %+v", issue)
				}
			}
			if h.count("artifacts") != 1 || h.count("attentions") != 1 {
				t.Fatal("the choice replaced the original report or decision")
			}
		})
	}
}
