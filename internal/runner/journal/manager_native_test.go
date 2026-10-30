package journal

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerJournalPreservesUnknownOperationsAndEventReceiptsAcrossRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	as := protocol.Assignment{RunID: "manager-run", LeaseID: "lease", Role: protocol.RoleIssueManager}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	command := protocol.ManagerNativeRequest{ID: "native-start", Method: "start", Payload: json.RawMessage(`{"config":{"Model":"manager-model"}}`)}
	if status, reply, err := j.ManagerOperation(ctx, as.RunID, command); err != nil || status != "received" || reply != nil {
		t.Fatal(status, reply, err)
	}
	if err := j.StartManagerOperation(ctx, command.ID); err != nil {
		t.Fatal(err)
	}
	event := &protocol.Envelope{RequestID: "receipt-1", Operation: protocol.OpManagerNativeEvent, Target: protocol.Target{Type: "run", ID: as.RunID}}
	if err := j.SaveManagerEvent(ctx, as.RunID, event, json.RawMessage(`{"thread_id":"native-thread"}`)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	if status, reply, err := j.ManagerOperation(ctx, as.RunID, command); err != nil || status != "started" || reply != nil {
		t.Fatal("restart made an uncertain native start replayable", status, reply, err)
	}
	requests, err := j.Requests(ctx)
	if err != nil || len(requests) != 1 || string(requests[0].Envelope.Payload) != string(event.Payload) {
		t.Fatal("event retry identity changed", requests, err)
	}
	if err := j.CompleteManagerOperation(ctx, protocol.ManagerNativeReply{ID: command.ID, Result: json.RawMessage(`{"ID":"native-thread"}`)}); err != nil {
		t.Fatal(err)
	}
	attachments, err := j.ManagerAttachments(ctx, as.RunID)
	if err != nil || len(attachments) != 1 || attachments[0].Method != "resume" {
		t.Fatal(attachments, err)
	}
	var resume struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(attachments[0].Payload, &resume); err != nil || resume.ThreadID != "native-thread" {
		t.Fatal("restart lost native ownership", resume, err)
	}
	next := &protocol.Envelope{RequestID: "receipt-2", Operation: protocol.OpManagerNativeEvent, Target: event.Target}
	if err := j.SaveManagerEvent(ctx, as.RunID, next, json.RawMessage(`{"disconnected":true}`)); err != nil {
		t.Fatal(err)
	}
	var payload protocol.ManagerNativeEvent
	if err := json.Unmarshal(next.Payload, &payload); err != nil || payload.Sequence != 2 {
		t.Fatal("restart reused a sequence", payload, err)
	}
	changed := command
	changed.Payload = json.RawMessage(`{"config":{"Model":"another-model"}}`)
	if _, _, err := j.ManagerOperation(ctx, as.RunID, changed); err == nil {
		t.Fatal("native request identity accepted different bytes")
	}
}
