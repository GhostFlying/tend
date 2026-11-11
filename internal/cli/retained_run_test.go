package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

const retainedCLIRun = "run_01ARZ3NDEKTSV4RRFFQ69G5FAV"
const retainedCLIRequest = "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func retainedCLIProof() protocol.ContinueRetainedRunPayload {
	identity := protocol.RetainedRunIdentity{TaskID: "task_01ARZ3NDEKTSV4RRFFQ69G5FAV", ActionID: "act_01ARZ3NDEKTSV4RRFFQ69G5FAV", Generation: 2, SpecVersion: 1, HostID: "runner-one", LeaseID: "lse_01ARZ3NDEKTSV4RRFFQ69G5FAV", Execution: protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner-one", LocalID: "cedar"}, AgentRevision: 2, Runtime: protocol.AgentRuntimeReference{RuntimeID: "original-runtime", Revision: 3}, InventoryRevision: 4, SnapshotID: "original-snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "original-model", Effort: "high"}, Worker: protocol.WorkerOrca, BackendID: "orca"}}
	protections := []protocol.RetainedRunProtection{}
	for _, id := range []string{"maintenance-a", "maintenance-b"} {
		protections = append(protections, protocol.RetainedRunProtection{MaintenanceID: id, RunID: retainedCLIRun, TaskID: identity.TaskID, ActionID: identity.ActionID, HostID: identity.HostID, LeaseID: identity.LeaseID, OriginalRequestsJSON: "[]", Linked: true})
	}
	protectionBytes, _ := json.Marshal(protections)
	protectionHash := sha256.Sum256(protectionBytes)
	sourceRevision := int64(5)
	return protocol.ContinueRetainedRunPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: "msg_01ARZ3NDEKTSV4RRFFQ69G5FAV", ExpectedSourceRevision: &sourceRevision}, RetainedRunIdentity: identity, Protections: protections, ProtectionSetSHA256: hex.EncodeToString(protectionHash[:]), Reason: "Attach only the independently authorized original native identity"}
}

func retainedCLIArgs(raw []byte) []string {
	return []string{"run", "continue-retained", retainedCLIRun, "--revision", "7", "--request-id", retainedCLIRequest, "--data", string(raw), "--json"}
}

func assertRetainedCLIEnvelope(t *testing.T, raw []byte, want protocol.ContinueRetainedRunPayload) protocol.Envelope {
	t.Helper()
	var env protocol.Envelope
	var got protocol.ContinueRetainedRunPayload
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if env.Operation != protocol.OpContinueRetainedRun || env.RequestID != retainedCLIRequest || env.Target != (protocol.Target{Type: "run", ID: retainedCLIRun}) || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || !reflect.DeepEqual(got, want) {
		t.Fatalf("original continuation identity changed: envelope=%+v proof=%+v", env, got)
	}
	return env
}

func TestRetainedRunCLIDiagnosticPreservesUnknownLedgerFacts(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	p := retainedCLIProof()
	want := protocol.RetainedRunReconciliation{RecoveryAuthorizationID: "original-force-authorization", RunID: retainedCLIRun, Identity: p.RetainedRunIdentity, TaskRevision: 7, Role: protocol.RoleImplement, Observed: protocol.RunUnknown, LeaseStatus: "lapsed", Protections: p.Protections, ProtectionSetSHA256: p.ProtectionSetSHA256, OriginalRequestIDs: []string{"original-business"}, UnresolvedNativeRequests: 1, UnappliedEvents: 3, PendingBusinessReceipts: true, Blockers: []string{"native_requests_unreconciled", "native_events_unreconciled"}}
	route := "GET /api/v1/runs/" + retainedCLIRun + "/maintenance-reconciliation"
	f.answer(route, want)
	if code := te.main("run", "maintenance-reconciliation", retainedCLIRun, "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var got protocol.RetainedRunReconciliation
	if err := json.Unmarshal(te.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ledger read dropped unknown/identity facts: %+v %v", got, err)
	}
	if len(f.seen(route)) != 1 || len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("diagnostic performed an unexpected write/read")
	}
}

func TestRetainedRunCLIDataFileDryRunAndExplicitReplayPreserveIdentity(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	want := retainedCLIProof()
	want.RecoveryAuthorizationID = "original-force-authorization"
	raw, _ := json.Marshal(want)
	args := retainedCLIArgs(raw)
	if code := te.main(append(slices.Clone(args), "--dry-run")...); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	original := assertRetainedCLIEnvelope(t, te.out.Bytes(), want)
	if len(f.requests) != 0 {
		t.Fatal("dry-run contacted server")
	}
	frozen := filepath.Join(t.TempDir(), "original-envelope.json")
	if err := os.WriteFile(frozen, slices.Clone(te.out.Bytes()), 0600); err != nil {
		t.Fatal(err)
	}
	payloadFile := filepath.Join(t.TempDir(), "proof.json")
	if err := os.WriteFile(payloadFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"continuation_request_id":"` + retainedCLIRequest + `","task_revision":8,"pending_business_receipts":false}`)
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		actual := assertRetainedCLIEnvelope(t, body, want)
		a, err := protocol.PayloadHash(&actual)
		if err != nil {
			t.Error(err)
		}
		b, err := protocol.PayloadHash(&original)
		if err != nil || a != b {
			t.Error("explicit submission changed original intent hash", a, b, err)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: actual.RequestID, Status: protocol.OpSucceeded, Result: result}})
	})
	for _, call := range [][]string{args, append(slices.Clone(args[:len(args)-3]), "--file", payloadFile, "--json"), {"operation", "submit", "--file", frozen, "--json"}} {
		te.out.Reset()
		te.errOut.Reset()
		if code := te.main(call...); code != exitOK {
			t.Fatal(call, code, te.errOut.String())
		}
		var got protocol.APIResponse
		if err := json.Unmarshal(te.out.Bytes(), &got); err != nil || got.Operation == nil || got.Operation.RequestID != retainedCLIRequest || got.Operation.Status != protocol.OpSucceeded {
			t.Fatal("complete receipt not preserved", got, err)
		}
	}
	sent := f.seen("POST /api/v1/ops")
	if len(sent) != 3 || !bytes.Equal(sent[0].body, sent[1].body) || !bytes.Equal(sent[1].body, sent[2].body) {
		t.Fatal("explicit original data/file/replay changed envelope bytes or repeated submission", len(sent))
	}
}

func TestRetainedRunCLIRequiresExplicitControlRevision(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	raw, _ := json.Marshal(retainedCLIProof())
	args := retainedCLIArgs(raw)
	args = append(slices.Clone(args[:3]), args[5:]...)
	if code := te.main(args...); code != exitUsage {
		t.Fatal("missing original CAS accepted", code, te.errOut.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("missing CAS refreshed state or submitted a write")
	}
}

func TestRetainedRunCLIUnknownRequiresReadBeforeExplicitReplay(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	want := retainedCLIProof()
	raw, _ := json.Marshal(want)
	args := retainedCLIArgs(raw)
	if code := te.main(append(slices.Clone(args), "--dry-run")...); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	frozen := filepath.Join(t.TempDir(), "original-envelope.json")
	frozenBytes := slices.Clone(te.out.Bytes())
	if err := os.WriteFile(frozen, frozenBytes, 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		assertRetainedCLIEnvelope(t, body, want)
		// The application has durably accepted the original request; its reply is lost.
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	})
	te.out.Reset()
	te.errOut.Reset()
	if code := te.main(args...); code != exitError {
		t.Fatal("lost receipt not reported", code, te.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("unknown continuation implicitly resubmitted")
	}
	if !strings.Contains(te.errOut.String(), "unknown_outcome") || !strings.Contains(te.errOut.String(), "run maintenance-reconciliation "+retainedCLIRun) {
		t.Fatal("unknown result lost original request recovery guidance", te.errOut.String())
	}
	if saved, err := os.ReadFile(frozen); err != nil || !bytes.Equal(saved, frozenBytes) {
		t.Fatal("original local envelope changed", err)
	}
	wantState := protocol.RetainedRunReconciliation{RunID: retainedCLIRun, Identity: want.RetainedRunIdentity, TaskRevision: 8, Observed: protocol.RunUnknown, UnresolvedNativeRequests: 1, PendingBusinessReceipts: true}
	path := "GET /api/v1/runs/" + retainedCLIRun + "/maintenance-reconciliation"
	f.answer(path, wantState)
	te.out.Reset()
	te.errOut.Reset()
	if code := te.main("run", "maintenance-reconciliation", retainedCLIRun, "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var view protocol.RetainedRunReconciliation
	if err := json.Unmarshal(te.out.Bytes(), &view); err != nil || !reflect.DeepEqual(view, wantState) {
		t.Fatal("actual retained state unavailable", view, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 1 || len(f.seen(path)) != 1 {
		t.Fatal("unknown native operation was resubmitted during reconciliation")
	}

}

func TestRetainedRunCLIHelpAndCapabilityDescribeCompleteFrozenInput(t *testing.T) {
	te := newTestEnv(t, "")
	if code := te.main("run", "continue-retained", "--help"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	help := strings.ToLower(te.out.String())
	for _, term := range []string{"revision", "request-id", "file", "data", "dry-run", "protection", "strict", "maintenance-reconciliation"} {
		if !strings.Contains(help, term) {
			t.Errorf("leaf help omitted %q", term)
		}
	}
	found := false
	for _, b := range CapabilityBindings() {
		if b.Capability != protocol.OpContinueRetainedRun {
			continue
		}
		found = true
		if b.Command != "run continue-retained" {
			t.Fatal("wrong named CLI capability", b)
		}
		at := slices.Index(b.Example, "--data")
		if at < 0 || at+1 >= len(b.Example) {
			t.Fatal("capability lacks complete proof example", b)
		}
		var p protocol.ContinueRetainedRunPayload
		if err := json.Unmarshal([]byte(b.Example[at+1]), &p); err != nil || p.TaskID == "" || p.ActionID == "" || p.LeaseID == "" || p.Generation < 1 || p.SpecVersion < 1 || p.Execution.Validate(p.HostID) != nil || len(p.Protections) == 0 || len(p.ProtectionSetSHA256) != 64 || p.Reason == "" {
			t.Fatal("capability publishes invalid/incomplete continuation proof", p, err)
		}
		if slices.Index(b.Example, "--revision") < 0 {
			t.Fatal("capability omitted Task CAS")
		}
	}
	if !found {
		t.Fatal("continuation capability absent")
	}
}

func TestRetainedRunCLIRejectedOperationStaysSingleAttempt(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("POST /api/v1/ops", protocol.APIResponse{Operation: &protocol.Operation{RequestID: retainedCLIRequest, Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeStaleRevision, "original Task control changed")}})
	raw, _ := json.Marshal(retainedCLIProof())
	if code := te.main(retainedCLIArgs(raw)...); code != exitError {
		t.Fatal("business rejection not preserved", code, te.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 || !strings.Contains(te.errOut.String(), "stale_revision") || strings.Contains(te.errOut.String(), "unknown_outcome") {
		t.Fatal("rejection was retried or misclassified", te.errOut.String())
	}
}
