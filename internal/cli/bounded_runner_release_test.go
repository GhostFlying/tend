package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBoundedRunnerAdmissionReleasePreservesExactSuccessorAndUnknown(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	raw := boundedRunnerAdmissionReleaseExample()
	var want protocol.BoundedRunnerAdmissionRelease
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Error(err)
		}
		var got protocol.BoundedRunnerAdmissionRelease
		if err := json.Unmarshal(env.Payload, &got); err != nil {
			t.Error(err)
		}
		if env.Operation != protocol.OpReleaseBoundedRunnerAdmission || env.RequestID != want.RequestID || env.Target != (protocol.Target{Type: "host", ID: want.HostID}) || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != want.ExpectedControlRevision || !reflect.DeepEqual(got, want) {
			t.Error("release changed original host/CAS/successor/journal facts", env, got)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpUnknown}})
	})
	args := []string{"host", "release-bounded-admission", want.HostID, "--data", raw, "--json"}
	if code := e.main(args...); code != exitError {
		t.Fatal("unknown release became success", code, e.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 || len(f.seen("GET /api/v1/snapshot")) != 0 {
		t.Fatal("release silently refreshed CAS or retried", f.requests)
	}
	if code := e.main(args...); code != exitError {
		t.Fatal("original release replay changed uncertainty", code)
	}
	seen := f.seen("POST /api/v1/ops")
	if len(seen) != 2 || string(seen[0].body) != string(seen[1].body) {
		t.Fatal("explicit retry changed original release intent", seen)
	}
	for _, conflict := range [][]string{{"--request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEY"}, {"--revision", "3"}, {"--revision", "-1"}} {
		if code := e.main(append(append([]string(nil), args...), conflict...)...); code != exitUsage || len(f.seen("POST /api/v1/ops")) != 2 {
			t.Fatal("conflicting release reached server", conflict, code)
		}
	}
}

func TestBoundedRunnerAdmissionReleaseDryRunAndHelpRetainQuarantine(t *testing.T) {
	e := newTestEnv(t, "")
	args := []string{"host", "release-bounded-admission", "sample", "--data", boundedRunnerAdmissionReleaseExample(), "--dry-run"}
	if code := e.main(args...); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var env protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &env); err != nil || env.Operation != protocol.OpReleaseBoundedRunnerAdmission || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 2 {
		t.Fatal("dry run lost original release Envelope", env, err)
	}
	e.out.Reset()
	if code := e.main("host", "release-bounded-admission", "--help"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	for _, term := range []string{"global admission", "old Run quarantine", "unknown", "successor", "journal", "request-id", "revision", "dry-run"} {
		if !strings.Contains(e.out.String(), term) {
			t.Fatal("release help omitted its actual boundary", term)
		}
	}
}
