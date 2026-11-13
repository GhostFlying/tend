package app

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func reportDeliveryCommunicationFixture(t *testing.T) (*harness, protocol.Issue, store.MainTurn, protocol.Principal, []protocol.ArtifactFile) {
	t.Helper()
	h, runtime := managerClaimHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAcceptanceEvidenceFiles)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := policyIssue(t, h, true)
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Investigate the original report", Spec: temporaryReportSpec()}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil || task.TaskID == "" {
		t.Fatal(op, err)
	}
	impl, ok := h.claimInputRun(runtime)
	if !ok || impl.TaskID != task.TaskID {
		t.Fatal("no original implementation", impl)
	}
	report, _ := h.uploadResultFile(impl, "report.md", []byte("# Original findings\n\nThe reported symptom was investigated.\n"))
	unfinalized, _ := h.uploadResultFile(impl, "draft.md", []byte("Unsubmitted draft"))
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Original investigation findings"}, Files: []protocol.ArtifactFile{report}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.Role != protocol.RoleAccept || accept.TaskID != task.TaskID {
		t.Fatal("no independent acceptance", accept)
	}
	evidence, _ := h.uploadResultFile(accept, "acceptance.md", []byte("# Independent acceptance\n\nThe original findings are supported.\n"))
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"summary":"independently checked"}`), Files: []protocol.ArtifactFile{evidence}}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("report was not accepted", h.task(task.TaskID))
	}
	resource := h.resourceForTask(task.TaskID)
	if !h.releaseResource(task.TaskID, resource.WorkspaceID) {
		t.Fatal("accepted investigation workspace did not release")
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task.TaskID, Action: "report", IssueControlPayload: protocol.IssueControlPayload{Reason: "The independent acceptance checked this original report"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Deliver the accepted original investigation"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	manager := reviewClaimManager(t, h, runtime)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	delivery, _, available, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil || !available {
		t.Fatal("accepted-result owner notification was not available", available, err)
	}
	turn := startHostedReview(t, h, "issue:"+issue.IssueID, delivery)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		message, err := store.GetMessage(t.Context(), tx.Q(), turn.MessageID)
		if err == nil && message.Author != "system" {
			t.Fatal("final reply is not routed to the original system notification", message.Author)
		}
		if err == nil {
			var external bool
			err = tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM messaging_messages WHERE message_id=?)`, turn.MessageID).Scan(&external)
			if external {
				t.Fatal("system notification unexpectedly acquired an external communication input")
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal := runPrincipal(manager.RunID, protocol.RoleIssueManager)
	principal.Thread, principal.ConversationID = turn.ThreadID, manager.Manager.ConversationID
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	close := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: "completed", Reason: "Finish this independently accepted investigation", Evidence: []protocol.IssueEvidence{{Kind: "owner_decision", Reference: turn.MessageID, Summary: "The original admitted responsibility is ending"}}}}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		basis, err := completedTaskEvidence(t.Context(), tx.Q(), task.TaskID, "The original investigation was independently accepted")
		if err != nil {
			return err
		}
		close.Closure.Evidence = []protocol.IssueEvidence{basis}
		pending, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		for _, review := range pending {
			close.CompletionReviews = append(close.CompletionReviews, protocol.CompletionReviewAcknowledgement{EventID: review.EventID, Reason: "Reviewed the original accepted investigation"})
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), close, CanonicalOwnerIssueCommand{Action: "close"}); op.Status != protocol.OpSucceeded {
		raw, _ := json.Marshal(op)
		t.Fatal(string(raw))
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := hostedEffect(t, h, principal, turn, 3, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, principal, turn, 4); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, issue, turn, principal, []protocol.ArtifactFile{report, evidence, unfinalized}
}

func TestReportDeliveryClosedIssuePublishesOriginalResultFiles(t *testing.T) {
	for _, attachments := range []string{"none", "report", "acceptance", "both", "unsubmitted"} {
		t.Run(attachments, func(t *testing.T) {
			h, issue, turn, principal, files := reportDeliveryCommunicationFixture(t)
			ids := []string{files[0].FileID}
			if attachments == "none" {
				ids = nil
			}
			if attachments == "acceptance" {
				ids = []string{files[1].FileID}
			}
			if attachments == "both" {
				ids = append(ids, files[1].FileID)
			}
			if attachments == "unsubmitted" {
				ids = []string{files[2].FileID}
			}
			artifacts, acceptances := h.count("artifacts"), h.count("acceptances")
			payload := protocol.CoordinationReply{ReplyTo: turn.MessageID, Body: "The investigation is complete; read the original findings.", Purpose: "final", Disposition: "linked", WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}, ReviewedKinds: allFactKinds, Attachments: ids}
			env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, payload, nil)
			op, err := h.app.Execute(t.Context(), principal, env, HostedCoordinationCommand{Command: CoordinationReply{}, ExecutionID: turn.RequestID, EffectSeq: 5})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatalf("original final reply was not published: error=%v operation=%+v", err, op)
			}
			id := messageID(t, op)
			messages := h.count("conversation_messages")
			replayed, err := h.app.Execute(t.Context(), principal, env, HostedCoordinationCommand{Command: CoordinationReply{}, ExecutionID: turn.RequestID, EffectSeq: 5})
			if err != nil || replayed.Error == nil || replayed.Error.Code != protocol.CodeDuplicateEffect || h.count("conversation_messages") != messages {
				t.Fatal("original request replay duplicated publication", replayed, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var published, delivered, linked bool
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM messaging_messages WHERE message_id=? AND direction='publication' AND channel_id='web' AND reply_to=?),EXISTS(SELECT 1 FROM messaging_deliveries WHERE message_id=? AND channel_id='web' AND state='accepted'),EXISTS(SELECT 1 FROM message_work_items WHERE message_id=? AND work_item_id=? AND active=1)`, id, turn.MessageID, id, id, issue.IssueID).Scan(&published, &delivered, &linked); err != nil {
					return err
				}
				if !published || !delivered || !linked {
					t.Fatal("final reply did not reach the formal Issue chat", published, delivered, linked)
				}
				attached, err := store.ConversationAttachments(t.Context(), tx.Q(), id)
				if err != nil {
					return err
				}
				if len(attached) != len(ids) {
					t.Fatal("original attachment references were lost", attached)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if attachments == "unsubmitted" {
				meta, original, err := h.app.OpenConversationAttachment(t.Context(), files[2].FileID)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(original)
				_ = original.Close()
				if err != nil || string(body) != "Unsubmitted draft" || meta.SHA256 != files[2].SHA256 || meta.Size != files[2].Size {
					t.Fatal("ordinary supplement lost its original bytes", meta, string(body), err)
				}
			}
			if current := canonicalTaskIssue(t, h, issue.IssueID); current.State != protocol.IssueClosed || current.Revision != issue.Revision || h.count("artifacts") != artifacts || h.count("acceptances") != acceptances {
				t.Fatal("ordinary publication changed formal completion", current)
			}
		})
	}
}

func TestReportDeliveryRejectsForeignOriginalsAndMissingInput(t *testing.T) {
	for _, original := range []string{"foreign_report", "foreign_acceptance", "foreign_notification", "missing_input"} {
		t.Run(original, func(t *testing.T) {
			h, issue, turn, principal, files := reportDeliveryCommunicationFixture(t)
			originalNotification := turn.MessageID
			file := files[2]
			if original != "missing_input" {
				// Admit a different real responsibility; it cannot publish the
				// first Issue's immutable implementation or verdict originals.
				if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				_, configuration := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
				h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(configuration))
				issue = assignHostedManager(t, h, policyIssue(t, h, true))
				turn, principal = startHostedIssueReview(t, h, issue)
				file = files[0]
				if original == "foreign_acceptance" {
					file = files[1]
				}
			}
			messages := h.count("conversation_messages")
			payload := protocol.CoordinationReply{ReplyTo: turn.MessageID, Body: "Only authorized originals may be published.", Purpose: "final", Disposition: "linked", WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}, ReviewedKinds: allFactKinds, Attachments: []string{file.FileID}}
			if original == "foreign_notification" {
				payload.ReplyTo, payload.Attachments = originalNotification, nil
			}
			if original == "missing_input" {
				payload.ReplyTo, payload.Attachments = "missing-input", nil
			}
			op, err := h.app.Execute(t.Context(), principal, h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, payload, nil), HostedCoordinationCommand{Command: CoordinationReply{}, ExecutionID: turn.RequestID, EffectSeq: 5})
			if err == nil {
				err = op.Error
			}
			want := protocol.CodeForbidden
			if original == "missing_input" {
				want = protocol.CodeNotFound
			}
			if protocol.CodeOf(err) != want || h.count("conversation_messages") != messages {
				t.Fatal("invalid original was published or partly committed", op, err)
			}
		})
	}
}

func TestReportDeliveryClosedOriginalTurnCanRecordNonblockingAnomaly(t *testing.T) {
	for _, kind := range []string{"action", "information"} {
		t.Run(kind, func(t *testing.T) {
			h, issue, turn, principal, _ := reportDeliveryCommunicationFixture(t)
			payload := protocol.CreateAttentionPayload{Kind: kind, IssueID: issue.IssueID, SourceMessageID: turn.MessageID, Title: "The final chat publication failed; preserve its original request for investigation"}
			env := h.env("attentions.create", protocol.Target{Type: "attention"}, payload, nil)
			command := HostedCoordinationCommand{Command: AttentionCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 5}
			op, err := h.app.Execute(t.Context(), principal, env, command)
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal("closure prevented reporting the original delivery anomaly", op, err)
			}
			count := h.count("attentions")
			replayed, err := h.app.Execute(t.Context(), principal, env, command)
			if err != nil || replayed.Error == nil || replayed.Error.Code != protocol.CodeDuplicateEffect || h.count("attentions") != count {
				t.Fatal("anomaly request replay duplicated its receipt", replayed, err)
			}
			if current := canonicalTaskIssue(t, h, issue.IssueID); current.State != protocol.IssueClosed || current.Revision != issue.Revision {
				t.Fatal("anomaly receipt reopened or revised the Issue", current)
			}
		})
	}
}

func TestReportDeliveryClosedAnomalyKeepsOriginalAuthorityBoundaries(t *testing.T) {
	for _, boundary := range []string{"decision", "blocking", "invalid_kind", "other_issue", "thread", "run", "expired_lease"} {
		t.Run(boundary, func(t *testing.T) {
			h, issue, turn, principal, _ := reportDeliveryCommunicationFixture(t)
			payload := protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Record an original delivery anomaly"}
			want := protocol.CodeForbidden
			switch boundary {
			case "decision":
				payload.Kind, want = "decision", protocol.CodeInvalidTransition
			case "blocking":
				payload.BlocksClose, want = true, protocol.CodeInvalidTransition
			case "invalid_kind":
				payload.Kind, want = "problem", protocol.CodeInvalidRequest
			case "other_issue":
				payload.IssueID = policyIssue(t, h, true).IssueID
			case "thread":
				principal.Thread = "different-thread"
			case "run":
				principal.Subject = "different-run"
			case "expired_lease":
				h.clock.Advance(24 * time.Hour)
			}
			count := h.count("attentions")
			op, err := h.app.Execute(t.Context(), principal, h.env("attentions.create", protocol.Target{Type: "attention"}, payload, nil), HostedCoordinationCommand{Command: AttentionCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 5})
			if err == nil {
				err = op.Error
			}
			if protocol.CodeOf(err) != want || h.count("attentions") != count {
				t.Fatal("anomaly receipt escaped original authority", op, err)
			}
		})
	}
}
