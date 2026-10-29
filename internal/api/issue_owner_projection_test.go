package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestOwnerIssueProjectionPreservesOriginalsAndAssociationCAS(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	original := []byte("Original owner material\nwith an exact final LF\n")
	query := url.Values{"name": {"original.txt"}, "media_type": {"text/plain"}, "request_id": {e.ids.New(protocol.PrefixRequest)}}
	req, _ := http.NewRequest("POST", e.url+"/api/v1/conversation/attachments?"+query.Encode(), bytes.NewReader(original))
	req.Header.Set("Authorization", "Bearer "+token)
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(deploymentHeader, deployment)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var upload protocol.APIResponse
	if err := json.NewDecoder(response.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || upload.Operation == nil || upload.Operation.Status != protocol.OpSucceeded {
		t.Fatal(response.StatusCode, upload)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(upload.Operation.Result, &file); err != nil {
		t.Fatal(err)
	}
	body := "Owner original: preserve punctuation, work_item_id text, and attachments."
	env := e.envelope("conversation.submit_message", protocol.Target{Type: "message"}, nil, protocol.SubmitMessagePayload{Body: body, Attachments: []string{file.FileID}})
	op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &env, app.SubmitMessage{})
	if err != nil || op.Status != protocol.OpAccepted {
		t.Fatal(op, err)
	}
	var submitted struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &submitted); err != nil {
		t.Fatal(err)
	}
	var before protocol.IssueMessageAssociations
	path := "/api/v1/messages/" + submitted.MessageID + "/associations?projection=issues"
	if status := e.do("GET", path, token, nil, &before); status != http.StatusOK {
		t.Fatal(status, path)
	}
	// A queued message has not become a transcript block and is not a manual
	// source, but the owner must be able to inspect its actual original.
	var queuedOriginal protocol.IssueConversationItem
	detailPath := "/api/v1/messages/" + submitted.MessageID
	if status := e.do("GET", detailPath, token, nil, &queuedOriginal); status != http.StatusOK || queuedOriginal.Body != body || queuedOriginal.Author != "owner" || len(queuedOriginal.Attachments) != 1 || queuedOriginal.Attachments[0].FileID != file.FileID {
		t.Fatal("original source detail missing", status, queuedOriginal)
	}
	if status := e.do("GET", "/api/v1/conversation/blocks/"+submitted.MessageID, token, nil, nil); status != http.StatusNotFound {
		t.Fatal("fixture already has a block", status)
	}
	if status := e.do("GET", detailPath, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal("original source is not owner-only", status)
	}
	if status := e.do("GET", "/api/v1/messages/"+e.ids.New(protocol.PrefixMessage), token, nil, nil); status != http.StatusNotFound {
		t.Fatal("missing source did not return404", status)
	}
	// A real pre-cutover task's legacy responsibility remains historical,
	// never a fabricated canonical Issue identity in the new projection.
	legacyTask := e.task("Historical legacy responsibility")
	var legacyDetail protocol.TaskDetail
	if status := e.do("GET", "/api/v1/tasks/"+legacyTask, token, nil, &legacyDetail); status != http.StatusOK || len(legacyDetail.WorkItemIDs) == 0 {
		t.Fatal(status, legacyDetail)
	}
	legacyID := legacyDetail.WorkItemIDs[0]
	legacyCommand := e.envelope("conversation.set_associations", protocol.Target{Type: "message", ID: submitted.MessageID}, &before.Revision, protocol.SetMessageAssociationsPayload{Disposition: "linked", ContentRevision: before.ContentRevision, WorkItemIDs: []string{legacyID}})
	// Explicit pre-cutover association fixture. This old operation is not a
	// current HTTP route; the actual canonical read must still preserve originals.
	if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &legacyCommand, app.SetMessageAssociations{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	if status := e.do("GET", path, token, nil, &before); status != http.StatusOK || len(before.Items) != 0 || before.Disposition != "linked" {
		t.Fatal("historical classification invented clickable Issue", status, before)
	}
	open := e.envelope("conversation.open_background", protocol.Target{Type: "work_item", ID: legacyID}, nil, struct{}{})
	opened, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &open, app.OpenAgentConversation{})
	if err != nil || opened.Status != protocol.OpSucceeded {
		t.Fatal(opened, err)
	}
	var logical struct {
		ID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(opened.Result, &logical); err != nil {
		t.Fatal(err)
	}
	var projectedConversation protocol.IssueAgentConversation
	if status := e.do("GET", "/api/v1/agent-conversations/"+logical.ID+"?projection=issues", token, nil, &projectedConversation); status != http.StatusOK || projectedConversation.IssueID != "" {
		t.Fatal("legacy chat invented an Issue", status, projectedConversation)
	}
	command := e.envelope("conversation.set_issue_associations", protocol.Target{Type: "message", ID: submitted.MessageID}, &before.Revision, protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: before.ContentRevision, IssueIDs: []string{issue.IssueID}})
	status, answer := e.op(token, command)
	if status != http.StatusOK || answer.Operation == nil || answer.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, answer)
	}
	requests := e.count("SELECT count(*) FROM request_records")
	if _, answer := e.op(token, command); answer.Operation == nil || answer.Operation.Status != protocol.OpSucceeded {
		t.Fatal(answer)
	}
	if requests != e.count("SELECT count(*) FROM request_records") {
		t.Fatal("replay wrote another receipt")
	}
	var associations protocol.IssueMessageAssociations
	if status := e.do("GET", path, token, nil, &associations); status != http.StatusOK || len(associations.Items) != 1 || associations.Items[0].IssueID != issue.IssueID || !associations.OwnerLocked {
		t.Fatal(status, associations)
	}
	stale := command
	stale.RequestID = e.ids.New(protocol.PrefixRequest)
	if _, reply := e.op(token, stale); reply.Operation == nil || reply.Operation.Error == nil || reply.Operation.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(reply)
	}
	var old protocol.ConversationPage
	var current protocol.IssueConversationPage
	// Dedicated raw history reader keeps persisted legacy identities. The
	// current default HTTP response uses canonical metadata, just like its explicit query.
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		old, err = views.Conversation(t.Context(), tx.Q(), 0, 0, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var defaults protocol.IssueConversationPage
	if status := e.do("GET", "/api/v1/conversation", token, nil, &defaults); status != http.StatusOK {
		t.Fatal(status)
	}
	if status := e.do("GET", "/api/v1/conversation?projection=issues", token, nil, &current); status != http.StatusOK {
		t.Fatal(status)
	}
	if !reflect.DeepEqual(defaults, current) {
		t.Fatal("default owner read retained a second legacy mode")
	}
	if len(old.Items) != len(current.Items) {
		t.Fatal("projection lost a conversation item", old, current)
	}
	found := false
	for i, item := range old.Items {
		projected := current.Items[i]
		if !reflect.DeepEqual(item.ConversationContent, projected.ConversationContent) {
			t.Fatal("projection changed original content/media or ordering", item.MessageID)
		}
		if item.MessageID != submitted.MessageID {
			continue
		}
		found = true
		if projected.Body != body || projected.Author != "owner" || len(projected.Attachments) != 1 || projected.Attachments[0].FileID != file.FileID {
			t.Fatal("projection changed original owner source", projected)
		}
		if item.Associations.Items[0].WorkItemID != projected.Associations.Items[0].IssueID {
			t.Fatal("projection renamed opaque identity")
		}
	}
	if !found {
		t.Fatal("original message missing")
	}
	var detailed protocol.IssueConversationItem
	if status := e.do("GET", detailPath, token, nil, &detailed); status != http.StatusOK || !reflect.DeepEqual(detailed.ConversationContent, queuedOriginal.ConversationContent) || detailed.Associations == nil || detailed.Associations.Revision != associations.Revision || len(detailed.Associations.Items) != 1 || detailed.Associations.Items[0].IssueID != issue.IssueID {
		t.Fatal("source detail changed original or lost association CAS", status, detailed)
	}

	var context protocol.IssueContextView
	if status := e.do("GET", "/api/v1/issues/"+issue.IssueID+"/context", token, nil, &context); status != http.StatusOK || len(context.Sources) != 1 || context.Sources[0].Associations.Items[0].IssueID != issue.IssueID {
		t.Fatal(status, context)
	}
	checkOwnerIssueQueueProjection(t, e, token, submitted.MessageID, issue.IssueID)

	var grant protocol.DownloadURLResponse
	if status := e.do("GET", "/api/v1/conversation/attachments/"+file.FileID+"/download-url", token, nil, &grant); status != http.StatusOK {
		t.Fatal(status)
	}
	downloaded, err := http.Get(e.url + grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(downloaded.Body)
	downloaded.Body.Close()
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("original bytes changed", err)
	}
	if status := e.do("GET", "/api/v1/conversation?projection=issues", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
}
