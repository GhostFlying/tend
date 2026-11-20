package journal

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerCloseDeliveryRollsBackWithOriginalSettlement(t *testing.T) {
	for _, failure := range []string{"enqueue", "retire"} {
		t.Run(failure, func(t *testing.T) {
			j := open(t, filepath.Join(t.TempDir(), "journal.db"))
			defer j.Close()
			as := protocol.Assignment{RunID: "actual-run", Protocol: 4, Role: protocol.RoleIssueManager}
			if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			command := protocol.ManagerNativeRequest{ID: "actual-close", Method: "close", Payload: json.RawMessage(`{}`)}
			if _, _, err := j.ManagerOperation(t.Context(), as.RunID, command); err != nil {
				t.Fatal(err)
			}
			reply := protocol.ManagerNativeReply{ID: command.ID, Method: "close", Result: json.RawMessage(`null`)}
			if err := j.CompleteManagerOperation(t.Context(), reply); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(reply)
			env := protocol.Envelope{RequestID: "original-close-envelope", Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: raw}
			if err := j.PutRequest(t.Context(), Request{RunID: as.RunID, Envelope: env}); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_close_settlement BEFORE INSERT ON manager_receipt_ack_ids BEGIN SELECT RAISE(ABORT,'injected ACK failure'); END`
			if failure == "retire" {
				trigger = `CREATE TRIGGER fail_close_settlement BEFORE DELETE ON requests BEGIN SELECT RAISE(ABORT,'injected retirement failure'); END`
			}
			if _, err := j.db.ExecContext(t.Context(), trigger); err != nil {
				t.Fatal(err)
			}
			if err := j.SettleManagerReceipt(t.Context(), as.RunID, env); err == nil {
				t.Fatal("injected failure did not abort")
			}
			delivered, err := j.ManagerCloseReplyDelivered(t.Context(), as.RunID, command.ID)
			pending, pendingErr := j.Requests(t.Context())
			ids, idsErr := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			if err != nil || pendingErr != nil || idsErr != nil || delivered || len(pending) != 1 || len(ids) != 0 {
				t.Fatal("partial close delivery fact escaped transaction", delivered, pending, ids, err, pendingErr, idsErr)
			}
		})
	}
}

func TestManagerCloseDeliveryMigrationDoesNotInferHistoricalReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	// Build the committed v24 journal before inserting an unacknowledged
	// close. Subsequent migrations must never infer peer acceptance.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[:24] {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.ExecContext(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	as := protocol.Assignment{RunID: "historical-run", Role: protocol.RoleIssueManager, Protocol: 3}
	command := protocol.ManagerNativeRequest{ID: "historical-close", Method: "close", Payload: json.RawMessage(`{}`)}
	assignment, _ := json.Marshal(as)
	request, _ := json.Marshal(command)
	reply, _ := json.Marshal(protocol.ManagerNativeReply{ID: command.ID, Result: json.RawMessage(`null`)})
	if _, err := legacy.ExecContext(t.Context(), `INSERT INTO runs(run_id,lease_id,run_token,assignment_json,phase,updated_at) VALUES(?,'','',?,'done',1)`, as.RunID, string(assignment)); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), `INSERT INTO manager_native_operations(request_id,run_id,request_json,reply_json,status) VALUES(?,?,?,?,'completed')`, command.ID, as.RunID, string(request), string(reply)); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), `PRAGMA user_version=24`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	j := open(t, path)
	defer j.Close()
	delivered, err := j.ManagerCloseReplyDelivered(t.Context(), as.RunID, command.ID)
	closed, closeErr := j.CompletedManagerClose(t.Context(), as.RunID)
	var version int
	if err := j.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err != nil || closeErr != nil || delivered || closed == nil || int64(version) != CurrentVersion {
		t.Fatal("migration fabricated historical acceptance or removed close proof", version, delivered, closed, err, closeErr)
	}
}
