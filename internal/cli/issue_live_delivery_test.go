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

func TestIssueMaterialsCLISelectsExactHistoricalDuty(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	const run = "run_01J00000000000000000000000"
	f.on("GET /api/v1/issues/original-issue/materials", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		query, err := protocol.ParseIssueMaterialsQuery(r.URL.Query())
		if err != nil || query.ID != run || query.DutyRevision != 3 {
			t.Error("historical read dropped the immutable selector", r.URL.RawQuery, err)
		}
		_ = json.NewEncoder(w).Encode(protocol.IssueMaterials{Kind: query.Kind, DeliveryAuthorization: &protocol.DeliveryExecutionAuthority{RunID: run, DutyRevision: 3, Historical: true, Valid: false}})
	})
	if code := e.main("issue", "materials", "original-issue", "--kind", "delivery_authorization", "--id", run, "--duty-revision", "3", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var result protocol.IssueMaterials
	if err := json.Unmarshal(e.out.Bytes(), &result); err != nil || result.DeliveryAuthorization == nil || result.DeliveryAuthorization.DutyRevision != 3 || result.DeliveryAuthorization.Valid {
		t.Fatal("CLI lost historical selection or changed its authority", result, err)
	}
}

func TestIssueHistoricalContributionCLIExportsImmutableSelector(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	const body = `{"action":"complete","task_id":"task_report","execution_task_id":"task_producer","execution_run_id":"run_01J00000000000000000000000","execution_duty_revision":3,"reason":"Settle only the selected original completed whole-PR delivery"}`
	if code := e.main("issue", "update-delivery", "original-issue", "--revision", "7", "--epoch", "3", "--data", body, "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var envelope protocol.Envelope
	var payload protocol.UpdateIssueDeliveryPayload
	if json.Unmarshal(e.out.Bytes(), &envelope) != nil || json.Unmarshal(envelope.Payload, &payload) != nil || payload.ExecutionDutyRevision != 3 || payload.ExecutionRunID != "run_01J00000000000000000000000" || envelope.Operation != "issues.update_delivery" || len(f.requests) != 0 {
		t.Fatal("dry-run changed the exact historical settlement or performed a write", envelope, payload)
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

func TestIssueContributionDeliverySettlementCLIPreservesOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.UpdateIssueDeliveryPayload{
		IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: 3, Reason: "Settle the accepted contribution using the completed whole PR execution"},
		TaskID:              "accepted-contribution", ExecutionTaskID: "settled-execution", Action: "complete", UpgradeID: "original-upgrade",
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if code := e.main("issue", "update-delivery", "original-issue", "--revision", "7", "--epoch", "3", "--data", string(payload), "--dry-run", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var env protocol.Envelope
	var got protocol.UpdateIssueDeliveryPayload
	if err := json.Unmarshal(e.out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Payload, &got); err != nil || !reflect.DeepEqual(got, want) || env.Operation != "issues.update_delivery" || env.Target.ID != "original-issue" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 {
		t.Fatal("historical settlement lost its original execution or Issue controls", env, got, err)
	}
	if len(f.requests) != 0 {
		t.Fatal("dry-run performed HTTP IO")
	}
	file := filepath.Join(t.TempDir(), "original-settlement.json")
	if err := os.WriteFile(file, e.out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var replay protocol.Envelope
		if err := json.Unmarshal(body, &replay); err != nil {
			t.Fatal(err)
		}
		oldHash, oldErr := protocol.PayloadHash(&env)
		newHash, newErr := protocol.PayloadHash(&replay)
		if oldErr != nil || newErr != nil || oldHash != newHash || replay.RequestID != env.RequestID || replay.Operation != env.Operation || replay.Target != env.Target || replay.ExpectedControlRevision == nil || *replay.ExpectedControlRevision != 7 {
			t.Error("replay replaced original settlement input", replay, oldErr, newErr)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	})
	e.out.Reset()
	if code := e.main("issue", "replay", "--file", file, "--json"); code != exitOK || len(f.requests) != 1 {
		t.Fatal("settlement replay performed implicit lookup or binding", code, e.errOut.String())
	}
}
