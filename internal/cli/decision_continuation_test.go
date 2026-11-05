package cli

import (
	"bytes"
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

const continuationCLIRequest = "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestDecisionContinuationCLICompleteRead(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.DecisionContinuationView{DecisionID: "question", RunID: "original-run", TaskID: "original-task", Revision: 3, DecisionVersion: 2, TaskControlRevision: 7,
		Policy: protocol.DecisionContinuationPolicy{Mode: protocol.DecisionDependency, Reason: "Await exact dependency", Dependency: &protocol.DecisionContinuationDependency{TaskID: "dependency-task", SpecVersion: 4}},
		State:  "waiting", ResumeCondition: "task_completed_at_spec", BlockedReason: "dependency_not_completed", AnswerRequestID: "original-answer"}
	f.answer("GET /api/v1/decisions/question/continuation", want)
	if code := e.main("decision-continuation", "show", "question", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got protocol.DecisionContinuationView
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("continuation metadata was lost", got, err)
	}
	if len(f.requests) != 1 || len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("continuation read submitted a write")
	}
}

func TestDecisionContinuationCLIDataFileDryRunAndOriginalReplay(t *testing.T) {
	for _, action := range []string{"resume", "reconcile"} {
		t.Run(action, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			payload := `{"expected_revision":3,"source_message_id":"original-source"}`
			args := []string{"decision-continuation", action, "question", "--revision", "7", "--request-id", continuationCLIRequest, "--data", payload, "--json"}
			if code := e.main(append(slices.Clone(args), "--dry-run")...); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			var original protocol.Envelope
			if err := json.Unmarshal(e.out.Bytes(), &original); err != nil || original.RequestID != continuationCLIRequest || original.Target != (protocol.Target{Type: "decision", ID: "question"}) || original.Operation != "decisions.continuation."+action || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 {
				t.Fatal(original, err)
			}
			var body protocol.ResumeDecisionContinuationPayload
			if err := json.Unmarshal(original.Payload, &body); err != nil || body.ExpectedRevision != 3 || body.SourceMessageID != "original-source" {
				t.Fatal(body, err)
			}
			if len(f.requests) != 0 {
				t.Fatal("dry-run contacted the server")
			}
			frozen := filepath.Join(t.TempDir(), "original.json")
			if err := os.WriteFile(frozen, slices.Clone(e.out.Bytes()), 0600); err != nil {
				t.Fatal(err)
			}
			payloadFile := filepath.Join(t.TempDir(), "payload.json")
			if err := os.WriteFile(payloadFile, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
				var actual protocol.Envelope
				if err := json.Unmarshal(raw, &actual); err != nil {
					t.Error("original envelope changed", actual, err)
				}
				gotHash, err := protocol.PayloadHash(&actual)
				wantHash, wantErr := protocol.PayloadHash(&original)
				if err != nil || wantErr != nil || gotHash != wantHash || actual.RequestID != original.RequestID {
					t.Error("original request intent changed", actual, gotHash, wantHash, err, wantErr)
				}
				_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: original.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"revision":4,"state":"waiting","blocked_reason":"original_process_not_stopped"}`)}})
			})
			for _, call := range [][]string{args, append(slices.Clone(args[:7]), "--file", payloadFile, "--json"), {"request", "replay", "--file", frozen, "--json"}} {
				e.out.Reset()
				e.errOut.Reset()
				if code := e.main(call...); code != exitOK {
					t.Fatal(call, code, e.errOut.String())
				}
				var response protocol.APIResponse
				if err := json.Unmarshal(e.out.Bytes(), &response); err != nil || response.Operation == nil || response.Operation.RequestID != continuationCLIRequest || !bytes.Contains(response.Operation.Result, []byte("original_process_not_stopped")) {
					t.Fatal("complete original receipt was lost", response, err)
				}
			}
			calls := f.seen("POST /api/v1/ops")
			if len(calls) != 3 || !bytes.Equal(calls[0].body, calls[1].body) || !bytes.Equal(calls[1].body, calls[2].body) {
				t.Fatal("data/file/original replay changed submitted bytes", calls)
			}
		})
	}
}

func TestDecisionContinuationCLIRejectsMissingCASAndCallerReadiness(t *testing.T) {
	for _, action := range []string{"resume", "reconcile"} {
		for _, flags := range [][]string{{"--data", `{"expected_revision":3}`}, {"--revision", "7", "--data", `{}`}, {"--revision", "7", "--data", `{"expected_revision":null}`}, {"--revision", "7", "--data", `{"expected_revision":-1}`}, {"--revision", "7", "--data", `{"expected_revision":3,"ready":true}`}} {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			args := append([]string{"decision-continuation", action, "question"}, flags...)
			if code := e.main(args...); code != exitUsage {
				t.Fatal(args, code, e.errOut.String())
			}
			if len(f.requests) != 0 {
				t.Fatal("invalid continuation input refreshed or mutated server state")
			}
		}
	}
}

func TestDecisionContinuationCLIUnknownKeepsOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	})
	if code := e.main("decision-continuation", "resume", "question", "--revision", "7", "--request-id", continuationCLIRequest, "--data", `{"expected_revision":3}`, "--json"); code != exitError {
		t.Fatal(code, e.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 || !strings.Contains(e.errOut.String(), "unknown_outcome") || !strings.Contains(e.errOut.String(), "request show "+continuationCLIRequest) {
		t.Fatal("unknown continuation was retried or lost its recovery identity", e.errOut.String())
	}
}

func TestDecideCLICompleteTypedPayloadAndContinuationHelp(t *testing.T) {
	for _, continuation := range []string{``, `,"continuation":{"mode":"continue_now"}`, `,"continuation":{"mode":"wait","reason":"Await explicit resume"}`, `,"continuation":{"mode":"dependency","reason":"Await exact completion","dependency":{"task_id":"dependency-task","spec_version":4}}`} {
		f := newFakeServer(t)
		e := newTestEnv(t, f.srv.URL)
		payload := `{"version":2,"text":"Actual owner answer"` + continuation + `}`
		if code := e.main("decide", "question", "--request-id", continuationCLIRequest, "--data", payload, "--dry-run"); code != exitOK {
			t.Fatal(code, e.errOut.String())
		}
		var env protocol.Envelope
		if err := json.Unmarshal(e.out.Bytes(), &env); err != nil || env.Operation != "decisions.respond" || env.ExpectedControlRevision != nil || env.RequestID != continuationCLIRequest {
			t.Fatal("complete typed answer changed", env, err)
		}
		var got, want any
		if err := json.Unmarshal(env.Payload, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(payload), &want); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("typed continuation fields were dropped", got, want, err)
		}
		if len(f.requests) != 0 {
			t.Fatal("answer dry-run contacted server")
		}
	}
	for _, command := range [][]string{{"decide", "--help"}, {"decision-continuation", "--help"}, {"decision-continuation", "resume", "--help"}, {"decision-continuation", "reconcile", "--help"}} {
		f := newFakeServer(t)
		e := newTestEnv(t, f.srv.URL)
		if code := e.main(command...); code != exitOK {
			t.Fatal(command, code, e.errOut.String())
		}
		for _, text := range []string{"answer only", "continue_now", "wait", "dependency", "expected_revision", "request replay"} {
			if !strings.Contains(e.out.String()+e.errOut.String(), text) {
				t.Fatal("help omitted continuation contract", command, text)
			}
		}
	}
}
