package runnerapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestQuestionInputsOwnerSourceMetadataAndOriginalRunBytes(t *testing.T) {
	e, as := httpQuestionRun(t)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	raw := []byte("original answer\x00evidence\xff")
	upload := func(name string) protocol.ConversationAttachment {
		t.Helper()
		op, err := e.app.UploadConversationAttachment(t.Context(), owner, e.ids.New(protocol.PrefixRequest), name, "application/octet-stream", bytes.NewReader(raw))
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		var f protocol.ConversationAttachment
		if err := json.Unmarshal(op.Result, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	file, other := upload("answer.bin"), upload("other.bin")
	var question protocol.WorkerQuestion
	if err := json.Unmarshal(e.mustOK(asRun(*as), e.envelope(protocol.OpRunsAskOwner, as.RunID, protocol.AskOwnerPayload{QuestionKey: "file", Question: "Can you supply the original?"})), &question); err != nil {
		t.Fatal(err)
	}
	execute := func(op string, p any, revision *int64, command app.Command) protocol.Operation {
		t.Helper()
		env := e.envelope(op, question.DecisionID, p)
		env.Target.Type = "decision"
		env.ExpectedControlRevision = revision
		result, err := e.app.Execute(t.Context(), owner, env, command)
		if err != nil || result.Status != protocol.OpSucceeded {
			t.Fatal(result, err)
		}
		return result
	}
	// Accept the original answer first; supplemental files use a different request.
	execute("decisions.respond", protocol.AnswerDecisionPayload{Version: question.Version, Text: "Use the attached original"}, nil, app.AnswerDecision{})
	var message struct {
		MessageID string `json:"message_id"`
	}
	payload, _ := json.Marshal(protocol.SubmitMessagePayload{MessageID: e.ids.New(protocol.PrefixMessage), Body: "Original file for that answer", ReplyTo: question.QuestionMessageID, Attachments: []string{file.FileID, other.FileID}})
	source, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "conversation.submit_message", Target: protocol.Target{Type: "conversation"}, Payload: payload}, app.SubmitMessage{})
	if err != nil || (source.Status != protocol.OpAccepted && source.Status != protocol.OpSucceeded) || json.Unmarshal(source.Result, &message) != nil {
		t.Fatal(source, err)
	}
	get := func(id string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.server.URL+"/runner/v1/files/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+as.RunToken)
		req.Header.Set(HeaderRun, as.RunID)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, body
	}
	if status, _ := get(file.FileID); status == http.StatusOK {
		t.Fatalf("ungranted file %d", status)
	}
	res, err := questionGET(t.Context(), e, asRun(*as), "/"+question.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	var answered protocol.WorkerQuestion
	if err := json.NewDecoder(res.Body).Decode(&answered); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	revision := int64(0)
	sourceRevision := int64(1)
	grant := protocol.QuestionInputPayload{Version: answered.Version, RunID: as.RunID, LeaseID: as.LeaseID, FileIDs: []string{file.FileID}, IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message.MessageID, ExpectedSourceRevision: &sourceRevision}, Reason: "Original owner answer evidence"}
	execute("decisions.grant_inputs", grant, &revision, app.QuestionInputCommand{})
	res, err = questionGET(t.Context(), e, asRun(*as), "/"+question.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	var withInput protocol.WorkerQuestion
	if err := json.NewDecoder(res.Body).Decode(&withInput); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if withInput.InputRevision != 1 || len(withInput.Inputs) != 1 || withInput.Inputs[0].FileID != file.FileID || withInput.Inputs[0].Name != file.Name || withInput.Inputs[0].SHA256 != file.SHA256 || !bytes.Equal(withInput.Answer, answered.Answer) {
		t.Fatal("worker metadata missing or old answer changed", withInput)
	}
	if status, body := get(withInput.Inputs[0].FileID); status != http.StatusOK || !bytes.Equal(body, raw) {
		t.Fatalf("exact worker original %d %q", status, body)
	}
	if status, _ := get(other.FileID); status == http.StatusOK {
		t.Fatal("unselected attachment shared", status)
	}
	revision = 1
	execute("decisions.revoke_inputs", protocol.QuestionInputPayload{GrantRevision: 1, Reason: "Owner revokes future reads"}, &revision, app.QuestionInputCommand{Revoke: true})
	if status, _ := get(file.FileID); status == http.StatusOK {
		t.Fatal("revoked file still readable", status)
	}
}
