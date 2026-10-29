package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestOwnerCreateRegisteredManagerIsAtomicAndReplayable(t *testing.T) {
	h := hostedManagerHarness(t)
	const body = "Original archived report for the selected manager"
	injected := h.exec(owner, h.env("conversation.inject_source", protocol.Target{Type: "message"}, protocol.InjectSourcePayload{Text: body, Metadata: protocol.ManualSourceMetadata{Author: "Original document author"}}, nil), InjectSource{})
	var original protocol.ManualSource
	if err := json.Unmarshal(injected.Result, &original); err != nil || original.MessageID == "" {
		t.Fatal(injected, err)
	}
	source := original.MessageID
	revision := h.associations(source).ContentRevision
	payload := protocol.CreateIssuePayload{
		Plan:                 protocol.IssuePlan{Title: "Owner selected manager", Project: "default", Intent: "Investigate the actual goal"},
		Assignee:             &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"},
		IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: &revision},
	}
	env := h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil)
	created := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "create"})
	issue := canonicalLifecycleIssue(t, created)
	if issue.Assignee != *payload.Assignee || issue.AssignmentEpoch != 1 {
		t.Fatal("selected manager was not assigned atomically", issue.IssueControl)
	}
	replay := canonicalLifecycleIssue(t, h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "create"}))
	if replay.IssueID != issue.IssueID || replay.Revision != issue.Revision {
		t.Fatal("same creation request created another Issue", issue, replay)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, query := range []string{
			`SELECT COUNT(*) FROM agent_conversations WHERE work_item_id=?`,
			`SELECT COUNT(*) FROM background_batches WHERE work_item_id=?`,
			`SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=?`,
		} {
			var count int
			if err := tx.Q().QueryRowContext(t.Context(), query, issue.IssueID).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Fatalf("creation/replay count %s = %d, want 1", query, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil || !ok {
		t.Fatal("new manager has no durable wake", ok, err)
	}
	conversation := "issue:" + issue.IssueID
	turn := startHostedReview(t, h, conversation, delivery)
	manager := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "manager", Thread: turn.ThreadID, ConversationID: conversation}
	command := BackgroundCommand{Action: "sources", MessageID: turn.MessageID, TurnID: turn.TurnID, DeliveryID: turn.RequestID}
	read := h.exec(manager, h.env(command.Operation(), protocol.Target{Type: "background_batch"}, map[string]int64{"after": 0}, nil), command)
	if read.Status != protocol.OpSucceeded {
		t.Fatal(read)
	}
	var page protocol.BackgroundSourcePage
	if err := json.Unmarshal(read.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Sources) != 1 || page.Sources[0].MessageID != source || page.Sources[0].Body != body {
		t.Fatal("selected manager did not receive the original source", page)
	}
	if got := canonicalTaskIssue(t, h, issue.IssueID); got.Plan.Intent != payload.Plan.Intent {
		t.Fatal("manager definition changed", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var cited string
		var citedRevision int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT source_message_id,json_extract(reason_json,'$.expected_source_revision') FROM issue_control_revisions WHERE issue_id=? AND revision=1`, issue.IssueID).Scan(&cited, &citedRevision); err != nil {
			return err
		}
		if cited != source || citedRevision != revision {
			t.Fatal("original source identity/revision lost", cited, citedRevision)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
