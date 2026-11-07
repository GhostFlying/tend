package runnerapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueContextRunDiscoversAndDownloadsOriginalOutsideSpec(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test-owner"}
	execute := func(cmd app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
		t.Helper()
		raw, _ := json.Marshal(payload)
		op, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: target, ExpectedControlRevision: revision, Payload: raw}, cmd)
		if err != nil || op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
			t.Fatal(cmd.Operation(), op, err)
		}
		return op
	}
	op := execute(app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Original material", Project: "default", Intent: "Inspect the Issue's original files"}}, nil)
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	original := []byte("GIF89a\x00original frames\xff")
	upload := func(name string) protocol.ConversationAttachment {
		t.Helper()
		op, err := e.app.UploadConversationAttachment(t.Context(), owner, e.ids.New(protocol.PrefixRequest), name, "image/gif", bytes.NewReader(original))
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		var file protocol.ConversationAttachment
		if err := json.Unmarshal(op.Result, &file); err != nil {
			t.Fatal(err)
		}
		return file
	}
	file, unrelated := upload("original.gif"), upload("unrelated.gif")
	message := execute(app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "Original material; reading it is not a new instruction", Attachments: []string{file.FileID}}, nil)
	var source struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(message.Result, &source); err != nil {
		t.Fatal(err)
	}
	execute(app.SetIssueMessageAssociations{}, protocol.Target{Type: "message", ID: source.MessageID}, protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: 1, IssueIDs: []string{issue.IssueID}}, new(int64))
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{WorkspaceProtocol: 1}))
	execute(app.CanonicalOwnerTaskCreate{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Inspect current material", Spec: protocol.OwnerTaskSpec{Goal: "Inspect originals", Acceptance: []string{"Cite actual original"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}, &issue.Revision)
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{WorkspaceProtocol: 1}))
	if as == nil {
		t.Fatal("no original implementation claim")
	}
	if strings.Contains(string(as.Spec), file.FileID) || strings.Contains(string(as.Spec), `"observation"`) {
		t.Fatal("fixture authorized its original through a spec snapshot", string(as.Spec))
	}
	request := func(path, token, run, host string) (int, []byte, http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(HeaderRun, run)
		req.Header.Set(HeaderHost, host)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, raw, res.Header
	}
	read := func(path string) (int, []byte, http.Header) { return request(path, as.RunToken, as.RunID, "") }
	if status, raw, _ := read("/runner/v1/context?kind=files"); status != http.StatusOK || !bytes.Contains(raw, []byte(file.FileID)) || bytes.Contains(raw, []byte(unrelated.FileID)) {
		t.Fatal("ordinary Issue original was not discoverable without spec permission", status, string(raw))
	}
	if status, raw, header := read("/runner/v1/files/" + file.FileID + "/metadata"); status != http.StatusOK || !bytes.Contains(raw, []byte(file.SHA256)) || header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("file metadata differs from indexed original", status, string(raw), header)
	}
	if status, raw, header := read("/runner/v1/issue-files/" + file.FileID); status != http.StatusOK || !bytes.Equal(raw, original) || header.Get("Content-Length") != strconv.Itoa(len(original)) || header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("original bytes changed", status, raw, header)
	}
	if status, _, _ := read("/runner/v1/files/" + file.FileID); status == http.StatusOK {
		t.Fatal("new general route silently changed legacy file semantics")
	}
	if status, _, _ := read("/runner/v1/observation"); status == http.StatusOK {
		t.Fatal("general context silently granted the old spec observation mode")
	}
	for _, path := range []string{"/runner/v1/files/" + unrelated.FileID + "/metadata", "/runner/v1/issue-files/" + unrelated.FileID} {
		if status, _, _ := read(path); status == http.StatusOK {
			t.Fatal("unrelated uploaded bytes became Issue material", path)
		}
	}
	if status, _, _ := request("/runner/v1/context", hostToken, "", "linux-1"); status != http.StatusForbidden {
		t.Fatal("host credential acquired arbitrary context", status)
	}
	if status, _, _ := request("/runner/v1/context", "incorrect", as.RunID, ""); status != http.StatusForbidden {
		t.Fatal("wrong credential acquired context", status)
	}
}
