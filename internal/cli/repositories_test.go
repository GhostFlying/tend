package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRepositoryRegistrationUsesCurrentRevisionAndStableRetry(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/repos/new", protocol.RepositoryRegistration{Repo: "new", RemoteURL: "https://example.com/new.git", ControlRevision: 4})
	count := 0
	f.on("POST /api/v1/ops", func(out http.ResponseWriter, _ *http.Request, raw []byte) {
		count++
		if count == 1 {
			conn, _, err := out.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 4 || env.Operation != "repos.register" || env.Reason == "" {
			t.Fatal(env)
		}
		_ = json.NewEncoder(out).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("repo", "register", "new", "--base", "main,release", "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	seen := f.seen("POST /api/v1/ops")
	if len(seen) != 2 || string(seen[0].body) != string(seen[1].body) {
		t.Fatal("transport retry changed identity")
	}
}

func TestRepositoryRegistrationPreservesUnspecifiedSettings(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/repos/new", protocol.RepositoryRegistration{Repo: "new", RemoteURL: "https://example.com/new.git", BaseCandidates: []string{"release"}, Retired: true, ControlRevision: 4})
	f.on("POST /api/v1/ops", func(out http.ResponseWriter, _ *http.Request, raw []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		var payload protocol.RegisterRepositoryPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if !payload.Retired || len(payload.BaseCandidates) != 1 || payload.BaseCandidates[0] != "release" {
			t.Fatal("unspecified settings changed", payload)
		}
		_ = json.NewEncoder(out).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("repo", "register", "new"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
}

func TestRepositoryReconciliationReadsOriginalOutcomeBeforeChangedDefaults(t *testing.T) {
	f := newFakeServer(t)
	requestID := "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	result, err := json.Marshal(protocol.RepositoryRegistration{Repo: "new", RemoteURL: "https://example.com/new.git", BaseCandidates: []string{"old-base"}, ControlRevision: 5})
	if err != nil {
		t.Fatal(err)
	}
	f.answer("GET /api/v1/requests/"+requestID, protocol.RequestView{RequestID: requestID, Operation: "repos.register", Status: protocol.OpSucceeded, Result: result})
	f.answer("GET /api/v1/repos/new", protocol.RepositoryRegistration{Repo: "new", BaseCandidates: []string{"later-base"}, ControlRevision: 8})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("repo", "register", "new", "--request-id", requestID, "--revision", "4", "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 0 || len(f.seen("GET /api/v1/repos/new")) != 0 {
		t.Fatal("reconciliation rebuilt or resent the mutation")
	}
	var answer protocol.APIResponse
	if err := json.Unmarshal(te.out.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Operation == nil || string(answer.Operation.Result) != string(result) {
		t.Fatal("original result not preserved", answer)
	}
}
