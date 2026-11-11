package journal

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerReceiptSettlementIsAtomicAcrossFailureAndRestart(t *testing.T) {
	for _, fail := range []string{"enqueue", "retire"} {
		t.Run(fail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.db")
			j := open(t, path)
			defer func() { _ = j.Close() }()
			as := protocol.Assignment{RunID: "original-run", Role: protocol.RoleIssueManager, Protocol: 4}
			if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			env := protocol.Envelope{RequestID: "original-reply", Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"id":"actual-command","method":"turn","result":"actual-turn"}`)}
			if err := j.PutRequest(t.Context(), Request{RunID: as.RunID, Envelope: env}); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_settlement BEFORE INSERT ON manager_receipt_ack_ids BEGIN SELECT RAISE(ABORT,'injected ACK write failure'); END;`
			if fail == "retire" {
				trigger = `CREATE TRIGGER fail_settlement BEFORE DELETE ON requests BEGIN SELECT RAISE(ABORT,'injected retirement failure'); END;`
			}
			if _, err := j.db.ExecContext(t.Context(), trigger); err != nil {
				t.Fatal(err)
			}
			if err := j.SettleManagerReceipt(t.Context(), as.RunID, env); err == nil {
				t.Fatal("injected journal failure did not abort settlement")
			}
			ids, err := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			pending, pendingErr := j.Requests(t.Context())
			if err != nil || pendingErr != nil || len(ids) != 0 || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, env) {
				t.Fatal("partial settlement escaped rollback", ids, pending, err, pendingErr)
			}
			if _, err := j.db.ExecContext(t.Context(), `DROP TRIGGER fail_settlement`); err != nil {
				t.Fatal(err)
			}
			if err := j.SettleManagerReceipt(t.Context(), as.RunID, env); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j = open(t, path)
			ids, err = j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			pending, pendingErr = j.Requests(t.Context())
			run, runErr := j.Run(t.Context(), as.RunID)
			if err != nil || pendingErr != nil || runErr != nil || len(pending) != 0 || !reflect.DeepEqual(ids, []string{"actual-command"}) || run.StoppedConfirmed || run.Phase != PhaseClaimed {
				t.Fatal("restart changed atomic delivery or physical execution", ids, pending, run, err, pendingErr, runErr)
			}
		})
	}
}

func TestManagerReceiptSettlementRejectsChangedOriginal(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	as := protocol.Assignment{RunID: "original-run"}
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	env := protocol.Envelope{RequestID: "original-reply", Operation: protocol.OpManagerNativeReply, Payload: json.RawMessage(`{"id":"actual-command","result":null}`)}
	if err := j.PutRequest(t.Context(), Request{RunID: as.RunID, Envelope: env}); err != nil {
		t.Fatal(err)
	}
	changed := env
	changed.Payload = json.RawMessage(`{"id":"different-command","result":null}`)
	if err := j.SettleManagerReceipt(t.Context(), as.RunID, changed); err == nil {
		t.Fatal("changed reply settled original")
	}
	ids, err := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
	pending, pendingErr := j.Requests(t.Context())
	if err != nil || pendingErr != nil || len(ids) != 0 || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, env) {
		t.Fatal(ids, pending, err, pendingErr)
	}
}
