package app

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueFinalPublicationUsesCurrentResponsibilityWithoutOldReplyEndpoint(t *testing.T) {
	for _, oldSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "no reply input", true: "retained source without endpoint"}[oldSource], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			turn, principal := startHostedIssueReview(t, h, issue)
			p := protocol.CoordinationReply{Body: "Background: long title overlapped the model. Result: the title now wraps at the tested phone widths. The reviewed fix has been delivered.", Purpose: "final"}
			if oldSource {
				p.ReplyTo = h.ids.New(protocol.PrefixMessage)
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: p.ReplyTo, ConversationID: "issue:" + issue.IssueID, Author: "system", Body: "Original delivery result retained without an incoming transport endpoint", CreatedAt: h.clock.Now().UnixMilli()})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var receipt struct {
				MessageID string `json:"message_id"`
				Mode      string `json:"publication_mode"`
			}
			if err := json.Unmarshal(op.Result, &receipt); err != nil || receipt.MessageID == "" || receipt.Mode != "notification" {
				t.Fatal("receipt did not distinguish actual notification from reply", string(op.Result), err)
			}
			message, err := h.app.MessagingMessage(t.Context(), receipt.MessageID)
			if err != nil || message.Body != p.Body || message.ReplyTo != "" || message.Purpose != "final" {
				t.Fatal("actual final report was not retained", message, err)
			}
		})
	}
}

func TestIssueParentPublishesFinalizedChildReviewOriginal(t *testing.T) {
	h := hostedManagerHarness(t)
	if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil), Hello{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	parent := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	payload := protocol.CreateIssuePayload{ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Plan: protocol.IssuePlan{Title: "Historical independent review", Project: parent.Plan.Project, Intent: "Return the original review report"}}
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	child = admitExistingIssueFixture(t, h, child)
	task := createCanonicalTaskForTest(t, h, child)
	run, ok, _ := h.claimDeliverableRun()
	if !ok || run.TaskID != task {
		t.Fatal(run)
	}
	file, _ := h.uploadResultFile(run, "review-report.md", []byte("Original independent review: tested the complete change."))
	result := testArtifact(h.task(task).WorkspaceName)
	result.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	turn, principal := startHostedIssueReview(t, h, parent)
	for effect := int64(1); effect <= 2; effect++ {
		p := protocol.CoordinationReply{Body: "The accepted review is attached as its original file.", Purpose: "final", Attachments: []string{file.FileID}}
		op := hostedEffect(t, h, principal, turn, effect, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal("parent cannot publish child result", op)
		}
		meta, body, err := h.app.OpenConversationAttachment(t.Context(), file.FileID)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || meta.SHA256 != file.SHA256 || string(bytes) != "Original independent review: tested the complete change." {
			t.Fatal(meta, string(bytes), err)
		}
	}
}
