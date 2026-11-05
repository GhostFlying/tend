package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func managerRetirementProofFlags() []string {
	return []string{"--issue", "issue-original", "--issue-revision", "4", "--assignment-epoch", "3", "--run", "run-original", "--host", "original-host", "--lease", "lease-original", "--pin-sha256", strings.Repeat("a", 64), "--reason", "Retire the original cancelling manager"}
}

func managerRetirementPayload(t *testing.T) protocol.RetireManagerRunPayload {
	t.Helper()
	var payload protocol.RetireManagerRunPayload
	if err := json.Unmarshal([]byte(`{"issue_id":"issue-original","expected_issue_revision":4,"expected_assignment_epoch":3,"run_id":"run-original","host_id":"original-host","lease_id":"lease-original","execution_sha256":"`+strings.Repeat("a", 64)+`","reason":"Retire the original cancelling manager"}`), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestManagerRetirementTypedProofAndDryRunKeepOriginalIdentity(t *testing.T) {
	te := newTestEnv(t, "")
	args := append([]string{"task", "retire-manager", "task-original", "--revision", "7", "--request-id", "req_01J00000000000000000000000", "--source-message", "owner-source", "--source-revision", "5", "--dry-run"}, managerRetirementProofFlags()...)
	if code := te.main(args...); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var env protocol.Envelope
	var actual protocol.RetireManagerRunPayload
	want := managerRetirementPayload(t)
	sourceRevision := int64(5)
	want.IssueSourceReference = protocol.IssueSourceReference{SourceMessageID: "owner-source", ExpectedSourceRevision: &sourceRevision}
	if err := json.Unmarshal(te.out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Payload, &actual); err != nil || !reflect.DeepEqual(actual, want) || env.Operation != protocol.OpRetireManagerRun || env.Target != (protocol.Target{Type: "task", ID: "task-original"}) || env.RequestID != "req_01J00000000000000000000000" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || env.Reason != want.Reason {
		t.Fatal("dry-run changed the original cancellation identity", env, actual, err)
	}
}

func TestManagerRetirementJSONFileAndStdinPreserveOriginalEnvelope(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	raw := `{"reason":"Retire the original cancelling manager","execution_sha256":"` + strings.Repeat("a", 64) + `","lease_id":"lease-original","host_id":"original-host","run_id":"run-original","expected_assignment_epoch":3,"expected_issue_revision":4,"issue_id":"issue-original","source_message_id":"source","expected_source_revision":5}`
	file := filepath.Join(t.TempDir(), "original-payload.json")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil || string(env.Payload) != raw || env.Reason != "Original envelope reason" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 {
			t.Error("complete input was rebuilt or its controls changed", env, err)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"retirement_request_id":"original","observed":"unknown","desired":"stopped","can_retire":false,"blockers":["physical_stop_unconfirmed"]}`)}})
	})
	base := []string{"task", "retire-manager", "task-original", "--revision", "7", "--request-id", "req_01J00000000000000000000000", "--reason", "Original envelope reason"}
	for _, input := range [][]string{{"--data", raw}, {"--file", file}, {"--file", "-"}} {
		te.out.Reset()
		te.Stdin = strings.NewReader(raw)
		if code := te.main(append(slices.Clone(base), input...)...); code != exitOK {
			t.Fatal(code, te.errOut.String())
		}
		if !strings.Contains(te.out.String(), `"observed": "unknown"`) || !strings.Contains(te.out.String(), `"physical_stop_unconfirmed"`) {
			t.Fatal("admission output lost physical uncertainty", te.out.String())
		}
	}
	seen := f.seen("POST /api/v1/ops")
	if len(seen) != 3 || !bytes.Equal(seen[0].body, seen[1].body) || !bytes.Equal(seen[1].body, seen[2].body) {
		t.Fatal("explicit original-request retry changed its envelope", seen)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 3 {
		t.Fatal("write refreshed the original control or proof", f.requests)
	}
}

func TestManagerRetirementRejectsIncompleteOrMixedProofBeforeHTTP(t *testing.T) {
	cases := []struct {
		name  string
		flags []string
	}{
		{"missing-task-revision", []string{"--revision", ""}},
		{"missing-issue", []string{"--issue", ""}},
		{"missing-issue-revision", []string{"--issue-revision", ""}},
		{"missing-epoch", []string{"--assignment-epoch", ""}},
		{"missing-run", []string{"--run", ""}},
		{"missing-host", []string{"--host", ""}},
		{"missing-lease", []string{"--lease", ""}},
		{"missing-pin", []string{"--pin-sha256", ""}},
		{"missing-reason", []string{"--reason", ""}},
		{"negative-issue-revision", []string{"--issue-revision", "-1"}},
		{"zero-issue-revision", []string{"--issue-revision", "0"}},
		{"bad-pin", []string{"--pin-sha256", "not-a-digest"}},
		{"bad-request", []string{"--request-id", "new-random-identity"}},
		{"source-without-revision", []string{"--source-message", "owner-source"}},
		{"revision-without-source", []string{"--source-revision", "5"}},
		{"mixed-data", []string{"--data", `{}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			args := append([]string{"task", "retire-manager", "task-original", "--revision", "7"}, managerRetirementProofFlags()...)
			index := slices.Index(args, tc.flags[0])
			if index >= 0 {
				args = append(args[:index], args[index+2:]...)
			}
			if tc.flags[1] != "" {
				args = append(args, tc.flags...)
			}
			if code := te.main(args...); code != exitUsage {
				t.Fatal("invalid proof did not fail locally", code, te.errOut.String())
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.requests) != 0 {
				t.Fatal("invalid proof contacted the service", f.requests)
			}
		})
	}
}

func TestManagerRetirementRejectsIncompleteCompleteInputBeforeHTTP(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{}`, `{"issue_id":"issue-original","expected_assignment_epoch":3,"run_id":"run-original","host_id":"original-host","lease_id":"lease-original","execution_sha256":"` + strings.Repeat("a", 64) + `","reason":"Original retirement"}`} {
		f := newFakeServer(t)
		te := newTestEnv(t, f.srv.URL)
		if code := te.main("task", "retire-manager", "task-original", "--revision", "7", "--data", input); code != exitUsage {
			t.Fatal("incomplete JSON did not fail locally", code, te.errOut.String())
		}
		f.mu.Lock()
		count := len(f.requests)
		f.mu.Unlock()
		if count != 0 {
			t.Fatal("incomplete JSON contacted the service", count)
		}
	}
}

func TestManagerRetirementStatusPreservesPaginationAndOriginalFacts(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	response := `{"task_id":"task-original","task_revision":7,"issue_id":"issue-original","issue_revision":4,"assignment_epoch":3,"run_id":"run-original","host_id":"original-host","lease_id":"lease-original","execution_sha256":"` + strings.Repeat("a", 64) + `","observed":"unknown","desired":"stopped","can_retire":false,"blockers":["physical_stop_unconfirmed"],"retirement_request_id":"retirement-original","native_requests":[{"request_id":"request-original","method":"turn","status":"unknown","result_present":true,"result_sha256":"` + strings.Repeat("b", 64) + `","error":true,"rejected":false}],"more":true,"next_after":"request-original"}`
	wantQuery := url.Values{"run_id": {"run-original"}, "limit": {"50"}}
	f.on("GET /api/v1/tasks/task-original/manager-retirement", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Encode() != wantQuery.Encode() {
			t.Error("original request cursor or bound changed", r.URL.Query(), wantQuery)
		}
		_, _ = w.Write([]byte(response))
	})
	for _, args := range [][]string{
		{"task", "manager-retirement", "task-original", "--run", "run-original", "--json"},
		{"task", "manager-retirement", "task-original", "--run", "run-original", "--after", "request-original", "--limit", "1", "--json"},
	} {
		te.out.Reset()
		if slices.Contains(args, "--after") {
			wantQuery.Set("after", "request-original")
			wantQuery.Set("limit", "1")
			response = `{"task_id":"task-original","run_id":"run-original","observed":"unknown","desired":"stopped","can_retire":false,"blockers":["physical_stop_unconfirmed"],"native_requests":[{"request_id":"request-original-later","method":"turn","status":"unknown","result_present":true,"result_hash_omitted":true,"error":true,"rejected":false}],"more":false}`
		}
		if code := te.main(args...); code != exitOK {
			t.Fatal(code, te.errOut.String())
		}
		var got, want any
		if json.Unmarshal(te.out.Bytes(), &got) != nil || json.Unmarshal([]byte(response), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("query dropped CAS, uncertainty, native identities or pagination", te.out.String())
		}
	}
	if len(f.seen("GET /api/v1/tasks/task-original/manager-retirement")) != 2 || len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("read command performed another operation")
	}
}

func TestManagerRetirementStatusRejectsUnboundedOrMutationInput(t *testing.T) {
	for _, flags := range [][]string{{}, {"--run", "run-original", "--limit", "0"}, {"--run", "run-original", "--limit", "101"}, {"--run", "run-original", "--limit", "-1"}, {"--run", "run-original", "--data", `{}`}} {
		f := newFakeServer(t)
		te := newTestEnv(t, f.srv.URL)
		if code := te.main(append([]string{"task", "manager-retirement", "task-original"}, flags...)...); code != exitUsage {
			t.Fatal("invalid read did not fail locally", flags, code, te.errOut.String())
		}
		f.mu.Lock()
		count := len(f.requests)
		f.mu.Unlock()
		if count != 0 {
			t.Fatal("invalid read contacted the service", flags, count)
		}
	}
}

func TestManagerRetirementLostReceiptUsesOriginalRequestRecovery(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	args := append([]string{"task", "retire-manager", "task-original", "--revision", "7", "--request-id", "req_01J00000000000000000000000"}, managerRetirementProofFlags()...)
	if code := te.main(append(slices.Clone(args), "--dry-run")...); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(te.out.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var sent protocol.Envelope
		if err := json.Unmarshal(body, &sent); err != nil || sent.RequestID != original.RequestID || sent.Operation != original.Operation || sent.Target != original.Target || sent.ExpectedControlRevision == nil || original.ExpectedControlRevision == nil || *sent.ExpectedControlRevision != *original.ExpectedControlRevision || sent.Reason != original.Reason || !sameCapabilityPayload(t, &sent, &original) {
			t.Error("original retirement envelope changed", sent, err)
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	})
	f.answer("GET /api/v1/requests/"+original.RequestID, protocol.RequestView{RequestID: original.RequestID, Operation: original.Operation, Status: protocol.OpSucceeded})
	te.out.Reset()
	if code := te.main(args...); code != exitError || !strings.Contains(te.errOut.String(), "unknown_outcome") || !strings.Contains(te.errOut.String(), "request show "+original.RequestID) {
		t.Fatal("lost response did not retain original reconciliation identity", code, te.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("unknown retirement was automatically repeated")
	}
	if code := te.main("request", "show", original.RequestID, "--json"); code != exitOK || len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("request reconciliation resubmitted retirement", code, te.errOut.String())
	}
}

func TestManagerRetirementHelpAndCapabilityUseCompleteProof(t *testing.T) {
	te := newTestEnv(t, "")
	if code := te.main("task", "retire-manager", "--help"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	for _, flag := range []string{"revision", "issue", "issue-revision", "assignment-epoch", "run", "host", "lease", "pin-sha256", "source-message", "source-revision", "reason", "data", "file", "request-id", "dry-run"} {
		if !strings.Contains(te.out.String(), "-"+flag) {
			t.Fatal("retirement help omitted input", flag, te.out.String())
		}
	}
	var write, read bool
	for _, binding := range CapabilityBindings() {
		switch binding.Capability {
		case protocol.OpRetireManagerRun:
			write = true
			for _, flag := range []string{"--revision", "--issue", "--issue-revision", "--assignment-epoch", "--run", "--host", "--lease", "--pin-sha256", "--reason"} {
				if !slices.Contains(binding.Example, flag) {
					t.Fatal("capability does not exercise complete typed proof", flag, binding)
				}
			}
		case "GET /api/v1/tasks/{id}/manager-retirement":
			read = slices.Contains(binding.Example, "--run")
		}
	}
	if !write || !read {
		t.Fatal("manager retirement capability bindings are incomplete", write, read)
	}
	te.out.Reset()
	if code := te.main("task", "manager-retirement", "--help"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	for _, flag := range []string{"-run", "-after", "-limit", "-json"} {
		if !strings.Contains(te.out.String(), flag) {
			t.Fatal("retirement query help omitted pagination", flag, te.out.String())
		}
	}
}
