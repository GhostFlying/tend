package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Called with a real uploaded owner source and canonical association from the
// detail regression. The queue, active turn and streaming blocks are actual App
// facts; this does not run a model or claim a complete coordination turn.
func checkOwnerIssueQueueProjection(t *testing.T, e *env, token, messageID, issueID string) {
	t.Helper()
	e.server.cfg.HostedMainAgent = true
	message := func(old protocol.ConversationItem, current protocol.IssueConversationItem) {
		t.Helper()
		if !reflect.DeepEqual(old.ConversationContent, current.ConversationContent) || !reflect.DeepEqual(old.Control, current.Control) || !reflect.DeepEqual(old.ReturnLink, current.ReturnLink) || !reflect.DeepEqual(old.Recovery, current.Recovery) || !reflect.DeepEqual(old.Choices, current.Choices) || old.ConversationID != current.ConversationID {
			t.Fatal("queue/block changed original content or identity", old.MessageID)
		}
		if old.Associations != nil {
			if current.Associations == nil || !reflect.DeepEqual(old.Associations.MessageAssociationState, current.Associations.MessageAssociationState) {
				t.Fatal("association CAS changed", old.MessageID)
			}
			if old.MessageID == messageID && (len(current.Associations.Items) != 1 || current.Associations.Items[0].IssueID != issueID) {
				t.Fatal("actual source lost canonical Issue", current)
			}
		}
	}
	entry := func(old protocol.ConversationQueueEntry, current protocol.IssueConversationQueueEntry) {
		t.Helper()
		message(old.Item, current.Item)
		if old.Position != current.Position || old.Revision != current.Revision || old.Status != current.Status || old.CanWithdraw != current.CanWithdraw {
			t.Fatal("queue control changed", old, current)
		}
	}
	queue := func(query string) protocol.ConversationQueuePage {
		t.Helper()
		var old protocol.ConversationQueuePage
		var current protocol.IssueConversationQueuePage
		path := "/api/v1/conversation/queue?conversation_id=main&" + query
		if status := e.do("GET", path, token, nil, &old); status != http.StatusOK {
			t.Fatal(status)
		}
		if status := e.do("GET", path+"&projection=issues", token, nil, &current); status != http.StatusOK {
			t.Fatal(status)
		}
		if old.Total != current.Total || old.Next != current.Next || old.EventSeq != current.EventSeq || old.Enabled != current.Enabled || len(old.Items) != len(current.Items) || (old.Active == nil) != (current.Active == nil) {
			t.Fatal("queue pagination/control changed", old, current)
		}
		for i, item := range old.Items {
			entry(item, current.Items[i])
		}
		if old.Active != nil {
			entry(*old.Active, *current.Active)
		}
		return old
	}
	block := func(old protocol.TranscriptBlock, current protocol.IssueTranscriptBlock) {
		t.Helper()
		message(old.Trigger, current.Trigger)
		if old.Position != current.Position || old.NoticeOnly != current.NoticeOnly || old.More != current.More || old.NextSeq != current.NextSeq || len(old.Items) != len(current.Items) {
			t.Fatal("block pagination changed", old, current)
		}
		for i, item := range old.Items {
			message(item, current.Items[i])
		}
	}
	readBlock := func(query string) protocol.TranscriptBlock {
		t.Helper()
		var old protocol.TranscriptBlock
		var current protocol.IssueTranscriptBlock
		path := "/api/v1/conversation/blocks/" + messageID + "?conversation_id=main&" + query
		if status := e.do("GET", path, token, nil, &old); status != http.StatusOK {
			t.Fatal(status)
		}
		if status := e.do("GET", path+"&projection=issues", token, nil, &current); status != http.StatusOK {
			t.Fatal(status)
		}
		block(old, current)
		return old
	}
	transcript := func(query string) {
		t.Helper()
		var old protocol.TranscriptPage
		var current protocol.IssueTranscriptPage
		path := "/api/v1/conversation/transcript?conversation_id=main&" + query
		if status := e.do("GET", path, token, nil, &old); status != http.StatusOK {
			t.Fatal(status)
		}
		if status := e.do("GET", path+"&projection=issues", token, nil, &current); status != http.StatusOK {
			t.Fatal(status)
		}
		if old.More != current.More || old.NextBefore != current.NextBefore || old.EventSeq != current.EventSeq || len(old.Blocks) != len(current.Blocks) {
			t.Fatal("transcript pagination changed", old, current)
		}
		for i, item := range old.Blocks {
			block(item, current.Blocks[i])
		}
	}
	queued := queue("category=owner")
	if len(queued.Items) == 0 || queued.Items[0].Item.MessageID != messageID {
		t.Fatal("original source is not queued", queued)
	}
	queue("category=owner&after=" + strconv.FormatInt(queued.Items[0].Position, 10))
	queue("category=background")
	delivery, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var payload app.HandleMessage
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil || payload.MessageID != messageID {
		t.Fatal(payload, err)
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: messageID, ThreadID: "queue-projection-native", Status: store.TurnStarting}
	if err := e.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "queue-projection-turn"
	if err := e.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	active := queue("category=owner")
	if active.Active == nil || active.Active.Item.MessageID != messageID {
		t.Fatal("missing active source", active)
	}
	for _, status := range []protocol.AssistantState{protocol.AssistantStreaming, protocol.AssistantCompleted} {
		env := e.envelope("conversation.record_agent_message", protocol.Target{Type: "conversation"}, nil, map[string]any{"body": "Actual streamed response: " + string(status), "reply_to": messageID, "turn_id": turn.TurnID, "item_id": "queue-projection-reply", "status": status})
		op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: turn.ThreadID}, &env, app.RecordAgentMessage{})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		b := readBlock("")
		if len(b.Items) != 1 || b.Items[0].Body != "Actual streamed response: "+string(status) {
			t.Fatal("block did not refresh real response", b)
		}
		var originalReply protocol.IssueConversationItem
		if status := e.do("GET", "/api/v1/messages/"+b.Items[0].MessageID, token, nil, &originalReply); status != http.StatusOK || !reflect.DeepEqual(originalReply.ConversationContent, b.Items[0].ConversationContent) {
			t.Fatal("reply original detail changed author or content", status, originalReply)
		}
		transcript("")
		transcript("before=" + strconv.FormatInt(b.Position, 10))
		if tail := readBlock("after=" + strconv.FormatInt(b.Items[0].Seq, 10)); len(tail.Items) != 0 {
			t.Fatal("after cursor ignored", tail)
		}
	}
	// Withdrawal remains the existing owner control, with its exact revision.
	env := e.envelope("conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: "Withdraw this queued follow-up"})
	status, answer := e.op(token, env)
	if status != http.StatusOK || answer.Operation == nil || answer.Operation.Status != protocol.OpAccepted {
		t.Fatal(status, answer)
	}
	var submitted struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(answer.Operation.Result, &submitted); err != nil {
		t.Fatal(err)
	}
	pending := queue("category=owner")
	var item protocol.ConversationQueueEntry
	for _, entry := range pending.Items {
		if entry.Item.MessageID == submitted.MessageID {
			item = entry
		}
	}
	if item.Item.MessageID == "" || !item.CanWithdraw {
		t.Fatal("missing exact queued follow-up", submitted.MessageID, pending)
	}
	withdraw := e.envelope("conversation.withdraw_message", protocol.Target{Type: "message", ID: item.Item.MessageID}, &item.Revision, protocol.WithdrawMessagePayload{})
	if status, answer := e.op(token, withdraw); status != http.StatusOK || answer.Operation == nil || answer.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, answer)
	}
	if withdrawn := queue("category=withdrawn"); len(withdrawn.Items) != 1 || withdrawn.Items[0].Status != "withdrawn" {
		t.Fatal(withdrawn)
	}
	for _, path := range []string{"/api/v1/conversation/queue?projection=issues", "/api/v1/conversation/transcript?projection=issues", "/api/v1/conversation/blocks/" + messageID + "?projection=issues"} {
		if status := e.do("GET", path, "", nil, nil); status != http.StatusUnauthorized {
			t.Fatal("owner-only projection", path, status)
		}
	}
}
