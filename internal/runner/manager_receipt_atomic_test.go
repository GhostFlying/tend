package runner

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestManagerPeerAcknowledgementNeverPrecedesLocalSettlement(t *testing.T) {
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	as := protocol.Assignment{RunID: "original-run", RunToken: "original-token", Protocol: 4, Role: protocol.RoleIssueManager}
	if err = j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	env := protocol.Envelope{RequestID: "original-reply", Operation: protocol.OpManagerNativeReply, Target: runTarget(as), Payload: json.RawMessage(`{"id":"actual-command","method":"start","result":{"id":"actual-thread"}}`)}
	if err = j.PutRequest(t.Context(), journal.Request{RunID: as.RunID, Envelope: env}); err != nil {
		t.Fatal(err)
	}
	replies, acks := 0, 0
	client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var got protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		switch got.Operation {
		case protocol.OpManagerNativeReply:
			replies++
		case protocol.OpManagerNativeClaim:
			acks++
			pending, err := j.Requests(t.Context())
			ids, idErr := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			if err != nil || idErr != nil || len(pending) != 0 || len(ids) != 1 || ids[0] != "actual-command" {
				t.Fatal("peer ACK preceded durable local settlement", pending, ids, err, idErr)
			}
			var payload protocol.ManagerNativeClaimPayload
			if json.Unmarshal(got.Payload, &payload) != nil || !payload.AcknowledgeOnly || len(payload.AcknowledgedIDs) != 1 {
				t.Fatal("ACK could dispatch work", got)
			}
			if acks == 1 {
				return nil, errors.New("peer consumed ACK before runner crash")
			}
		default:
			t.Fatal("unexpected operation", got.Operation)
		}
		return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: got.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})}
	r := New(Config{Journal: j, ServerURL: "http://runner.invalid", HTTPClient: client, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.DiscardHandler)})
	r.runnerProtocol = 4
	if err = r.sendManagerReceipt(t.Context(), as, &env); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash before the old caller can separately DeleteRequest.
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	r.cfg.Journal = j
	r.replayManagerAcknowledgements(t.Context())
	pending, err := j.Requests(t.Context())
	ids, idErr := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
	if err != nil || idErr != nil || len(pending) != 0 || len(ids) != 0 || replies != 1 || acks != 2 {
		t.Fatal("restart replayed settled native reply", pending, ids, replies, acks, err, idErr)
	}
}
