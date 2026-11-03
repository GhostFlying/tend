package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityManagerReconciliationPreservesProofAndRequest(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	want := protocol.ManagerRunReconciliation{TaskID: "task", RunID: "original", LeaseID: "lease", TaskRevision: 7, AssignmentEpoch: 3, Observed: protocol.RunUnknown, Blockers: []string{"business_receipts_unreconciled"}, PendingBusinessReceipts: true, CloseRequestID: "native-close", CloseResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	f.on("GET /api/v1/tasks/task/manager-reconciliation", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("run_id") != "original" {
			t.Error("diagnostic lost exact original Run")
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	if code := te.main("task", "manager-reconciliation", "task", "--run", "original", "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var got protocol.ManagerRunReconciliation
	if err := json.Unmarshal(te.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("unknown proof dropped by CLI", got, err)
	}
	p := protocol.ReconcileManagerRunPayload{RunID: want.RunID, LeaseID: want.LeaseID, CloseRequestID: want.CloseRequestID, CloseResultSHA256: want.CloseResultSHA256, ExpectedAssignmentEpoch: want.AssignmentEpoch, Reason: "Reconcile exact proved-close manager"}
	raw, _ := json.Marshal(p)
	request := "req_01J00000000000000000000000"
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		var actual protocol.ReconcileManagerRunPayload
		if err := json.Unmarshal(env.Payload, &actual); err != nil || !reflect.DeepEqual(actual, p) || env.Operation != protocol.OpReconcileManagerRun || env.Target != (protocol.Target{Type: "task", ID: "task"}) || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || env.RequestID != request {
			t.Error("CLI changed original proof/request", env, actual, err)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"responsibility":"waiting"}`)}})
	})
	args := []string{"task", "reconcile-manager", "task", "--revision", "7", "--request-id", request, "--data", string(raw), "--json"}
	for range 2 {
		if code := te.main(args...); code != exitOK {
			t.Fatal(code, te.errOut.String())
		}
	}
	seen := f.seen("POST /api/v1/ops")
	if len(seen) != 2 || string(seen[0].body) != string(seen[1].body) {
		t.Fatal("explicit retry changed original envelope", seen)
	}
}
