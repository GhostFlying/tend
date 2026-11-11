package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRepositoryRegistrationLostAnswerKeepsOriginalRequest(t *testing.T) {
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
	if code := te.main("repo", "register", "new", "--base", "main,release", "--json"); code != exitError {
		t.Fatal(code, te.errOut.String())
	}
	seen := f.seen("POST /api/v1/ops")
	if len(seen) != 1 {
		t.Fatal("unknown repository registration was automatically repeated")
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

func TestRepositoryRecoveryReadsActualBusinessStateWithoutRebuildingInput(t *testing.T) {
	f := newFakeServer(t)
	current := protocol.RepositoryRegistration{Repo: "new", RemoteURL: "https://example.com/new.git", BaseCandidates: []string{"later-base"}, ControlRevision: 8}
	f.answer("GET /api/v1/repos/new", current)
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("repo", "register", "new", "--request-id", "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--revision", "4", "--json"); code != exitUsage {
		t.Fatal("retained trace without original payload rebuilt a write", code, te.errOut.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("incomplete submission performed IO")
	}
	if code := te.main("repo", "show", "new", "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var answer protocol.RepositoryRegistration
	if err := json.Unmarshal(te.out.Bytes(), &answer); err != nil || answer.ControlRevision != current.ControlRevision || answer.BaseCandidates[0] != "later-base" {
		t.Fatal(answer, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 || len(f.seen("GET /api/v1/repos/new")) != 1 {
		t.Fatal("business state reconciliation submitted a write")
	}
}
