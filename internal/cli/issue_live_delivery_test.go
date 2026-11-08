package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueMaterialsCLIPreservesDeliveryExecutionAndSettlement(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.IssueMaterials{
		Kind: "delivery_authorization", IssueIDs: []string{"original-issue"}, Seq: 42, ObservedAt: 1234,
		DeliveryAuthorization: &protocol.DeliveryExecutionAuthority{
			TaskID: "original-task", SourceTaskID: "original-task", DutyRevision: 4, CurrentDutyRevision: 5,
			Valid: false, InvalidReason: "provider_candidate_changed",
			Settlement: &protocol.DeliverySettlementAuthority{Eligible: true, DutyRevision: 4, CurrentDutyRevision: 5, ArtifactID: "original-artifact", AcceptanceID: "original-acceptance", UpgradeID: "original-install"},
		},
	}
	f.on("GET /api/v1/issues/original-issue/materials", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("kind") != want.Kind || r.URL.Query().Get("id") != "original-task" {
			t.Error("delivery read lost its exact source", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	if code := e.main("issue", "materials", "original-issue", "--kind", want.Kind, "--id", "original-task", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got protocol.IssueMaterials
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("delivery read confused current authority and historical settlement", got, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("reading historical settlement performed a write")
	}
}

func TestIssueMaterialsCLIDeliveryAuthorityRequiresExactTask(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	if code := e.main("issue", "materials", "original-issue", "--kind", "delivery_authorization", "--json"); code != exitUsage {
		t.Fatal("owner read accepted an ambiguous Task", code, e.errOut.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("invalid owner selector performed HTTP IO")
	}
}

func TestIssueLiveDeliveryBindReplayKeepsBothControlRevisions(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	taskRevision := int64(11)
	want := protocol.UpdateIssueDeliveryPayload{
		IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: 3, Reason: "Continue this Task within the existing delivery scope"},
		TaskID:              "original-task", ExecutionTaskID: "original-task", ExpectedTaskControlRevision: &taskRevision, Action: "bind",
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if code := e.main("issue", "update-delivery", "original-issue", "--revision", "7", "--epoch", "3", "--data", string(payload), "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var original protocol.Envelope
	var got protocol.UpdateIssueDeliveryPayload
	if err := json.Unmarshal(e.out.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(original.Payload, &got); err != nil || !reflect.DeepEqual(got, want) || original.Operation != "issues.update_delivery" || original.Target.ID != "original-issue" || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 {
		t.Fatal("binding lost its existing Task or either control revision", original, got, err)
	}
	if len(f.requests) != 0 {
		t.Fatal("binding dry-run performed HTTP IO")
	}
	file := filepath.Join(t.TempDir(), "original-bind.json")
	if err := os.WriteFile(file, e.out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var replay protocol.Envelope
		if err := json.Unmarshal(body, &replay); err != nil {
			t.Fatal(err)
		}
		oldHash, oldErr := protocol.PayloadHash(&original)
		newHash, newErr := protocol.PayloadHash(&replay)
		if oldErr != nil || newErr != nil || oldHash != newHash || replay.RequestID != original.RequestID || replay.ExpectedControlRevision == nil || *replay.ExpectedControlRevision != 7 {
			t.Error("replay rebuilt the binding from new controls", replay, oldErr, newErr)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: original.RequestID, Status: protocol.OpSucceeded}})
	})
	e.out.Reset()
	if code := e.main("issue", "replay", "--file", file, "--json"); code != exitOK || len(f.requests) != 1 {
		t.Fatal("binding replay performed implicit control reads", code, e.errOut.String())
	}
}
