package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityManagerReconciliationPreservesProofAndRequest(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	want := protocol.ManagerRunReconciliation{TaskID: "task", RunID: "original", LeaseID: "lease", TaskRevision: 7, AssignmentEpoch: 3, Observed: protocol.RunUnknown, Blockers: []string{"business_receipts_unreconciled"}, PendingBusinessReceipts: true, CloseRequestID: "native-close", CloseResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StoppedRequestID: "native-stopped", StoppedPayloadSHA256: strings.Repeat("b", 64), StoppedLastSequence: 19}
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

func managerReceiptFlags() []string {
	return []string{"--mode", "read_receipts", "--run", "original", "--lease", "original-lease", "--close-request", "native-close", "--close-sha256", strings.Repeat("a", 64), "--stopped-request", "native-stopped", "--stopped-sha256", strings.Repeat("b", 64), "--assignment-epoch", "3", "--reason", "Read the original native receipt"}
}

func TestManagerReconciliationTypedModesPreserveProofAndRetry(t *testing.T) {
	for _, mode := range []string{"", "read_receipts"} {
		t.Run("mode="+mode, func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			sourceRevision := int64(5)
			want := protocol.ReconcileManagerRunPayload{Mode: mode, RunID: "original", LeaseID: "original-lease", CloseRequestID: "native-close", CloseResultSHA256: strings.Repeat("a", 64), ExpectedAssignmentEpoch: 3, Reason: "Read the original native receipt", IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: "owner-source", ExpectedSourceRevision: &sourceRevision}}
			flags := managerReceiptFlags()
			if mode == "" {
				flags = append(slices.Clone(flags[2:10]), flags[14:]...)
			} else {
				want.StoppedRequestID, want.StoppedPayloadSHA256 = "native-stopped", strings.Repeat("b", 64)
			}
			result := json.RawMessage(`{"responsibility":"waiting","observed":"finished"}`)
			if mode == "read_receipts" {
				result = json.RawMessage(`{"mode":"read_receipts","responsibility":"receipt_reading","observed":"failed","pending_request_ids":["original-native"]}`)
			}
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
				var env protocol.Envelope
				var actual protocol.ReconcileManagerRunPayload
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(env.Payload, &actual); err != nil || !reflect.DeepEqual(actual, want) || env.Operation != protocol.OpReconcileManagerRun || env.Target != (protocol.Target{Type: "task", ID: "task"}) || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || env.RequestID != "req_01J00000000000000000000000" || env.Reason != want.Reason {
					t.Error("typed input changed original reconciliation proof", env, actual, err)
				}
				_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}})
			})
			args := append([]string{"task", "reconcile-manager", "task", "--revision", "7", "--request-id", "req_01J00000000000000000000000", "--source-message", "owner-source", "--source-revision", "5", "--json"}, flags...)
			for range 2 {
				te.out.Reset()
				if code := te.main(args...); code != exitOK {
					t.Fatal(code, te.errOut.String())
				}
				var response protocol.APIResponse
				var actual, expected any
				if err := json.Unmarshal(te.out.Bytes(), &response); err != nil || response.Operation == nil {
					t.Fatal("CLI dropped the complete operation receipt", te.out.String(), err)
				}
				if json.Unmarshal(response.Operation.Result, &actual) != nil || json.Unmarshal(result, &expected) != nil || !reflect.DeepEqual(actual, expected) {
					t.Fatal("CLI changed reconciliation state or pending request identities", te.out.String())
				}
			}
			seen := f.seen("POST /api/v1/ops")
			if len(seen) != 2 || string(seen[0].body) != string(seen[1].body) {
				t.Fatal("typed retry changed the original envelope", seen)
			}
		})
	}
}

func TestManagerReconciliationRejectsIncompleteProofBeforeHTTP(t *testing.T) {
	for _, kind := range []string{"no-stop", "bad-sha", "bad-mode", "mixed-input", "no-revision", "source-revision"} {
		t.Run(kind, func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			args := append([]string{"task", "reconcile-manager", "task", "--revision", "7"}, managerReceiptFlags()...)
			switch kind {
			case "no-stop":
				args = append(args[:15], args[19:]...)
			case "bad-sha":
				args[slices.Index(args, "--close-sha256")+1] = "bad-digest"
			case "bad-mode":
				args[slices.Index(args, "--mode")+1] = "resume"
			case "mixed-input":
				args = append(args, "--data", `{}`)
			case "no-revision":
				args = append(args[:3], args[5:]...)
			case "source-revision":
				args = append(args, "--source-revision", "1")
			}
			if code := te.main(args...); code != exitUsage {
				t.Fatal("invalid proof did not fail locally", code, te.errOut.String())
			}
			if len(f.seen("POST /api/v1/ops")) != 0 {
				t.Fatal("invalid proof reached the owner mutation boundary")
			}
		})
	}
}

func TestManagerReconciliationRawReceiptFilePreservesOriginalEnvelope(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	raw := `{"reason":"Keep original bytes","mode":"read_receipts","run_id":"original","lease_id":"original-lease","close_request_id":"native-close","close_result_sha256":"` + strings.Repeat("a", 64) + `","stopped_request_id":"native-stopped","stopped_payload_sha256":"` + strings.Repeat("b", 64) + `","expected_assignment_epoch":3}`
	file := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil || string(env.Payload) != raw || env.Reason != "original envelope reason" {
			t.Error("raw proof was rebuilt or lost its original envelope reason", string(env.Payload), err)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})
	base := []string{"task", "reconcile-manager", "task", "--revision", "7", "--request-id", "req_01J00000000000000000000000", "--reason", "original envelope reason"}
	for _, input := range [][]string{{"--data", raw}, {"--file", file}, {"--file", file}} {
		if code := te.main(append(slices.Clone(base), input...)...); code != exitOK {
			t.Fatal(code, te.errOut.String())
		}
	}
	seen := f.seen("POST /api/v1/ops")
	if len(seen) != 3 || string(seen[0].body) != string(seen[1].body) || string(seen[1].body) != string(seen[2].body) {
		t.Fatal("raw/file retry changed the original envelope", seen)
	}
}

func TestManagerReconciliationHelpAndCapabilityUseTypedInput(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("task", "reconcile-manager", "--help"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	for _, text := range []string{"mode", "read_receipts", "run", "lease", "close-request", "close-sha256", "stopped-request", "stopped-sha256", "assignment-epoch", "source-message", "source-revision", "reason", "revision", "request-id", "file", "data", "dry-run"} {
		if !strings.Contains(te.out.String(), text) {
			t.Fatal("manager help omitted typed input", text, te.out.String())
		}
	}
	for _, binding := range CapabilityBindings() {
		if binding.Capability == protocol.OpReconcileManagerRun {
			if !slices.Contains(binding.Example, "--run") || !slices.Contains(binding.Example, "--stopped-request") || slices.Contains(binding.Example, "--data") {
				t.Fatal("capability did not exercise the typed receipt admission", binding)
			}
			return
		}
	}
	t.Fatal("manager reconciliation capability is absent")
}

func TestManagerReconciliationDryRunExportsCompleteOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	args := append([]string{"task", "reconcile-manager", "task", "--revision", "7", "--request-id", "req_01J00000000000000000000000", "--dry-run"}, managerReceiptFlags()...)
	if code := te.main(args...); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var env protocol.Envelope
	var payload protocol.ReconcileManagerRunPayload
	if json.Unmarshal(te.out.Bytes(), &env) != nil || json.Unmarshal(env.Payload, &payload) != nil || env.RequestID != "req_01J00000000000000000000000" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || payload.Mode != "read_receipts" || payload.StoppedRequestID != "native-stopped" || payload.StoppedPayloadSHA256 != strings.Repeat("b", 64) {
		t.Fatal("dry-run did not export the complete proof-bound request", te.out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("dry-run contacted the service", f.requests)
	}
}
