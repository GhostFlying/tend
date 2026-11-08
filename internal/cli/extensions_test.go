package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestExtensionsNamedReadsPreserveFiltersAndIdentity(t *testing.T) {
	for _, test := range []struct {
		args  []string
		view  string
		query map[string]string
	}{
		{[]string{"skill", "show", "fixture", "--version", "2", "--after", "cursor", "--limit", "3"}, "inventory", map[string]string{"kind": "skill", "id": "fixture", "version": "2", "after": "cursor", "limit": "3"}},
		{[]string{"plugin", "list"}, "inventory", map[string]string{"kind": "plugin"}},
		{[]string{"mcp", "show", "tools"}, "inventory", map[string]string{"kind": "mcp", "id": "tools"}},
		{[]string{"category", "show", "implementer"}, "categories", map[string]string{"id": "implementer"}},
		{[]string{"agent", "show", "worker", "--environment", "host-a"}, "agents", map[string]string{"id": "host-a/worker"}},
		{[]string{"resolve", "--category", "reviewer", "--environment", "host-b", "--agent", "worker", "--release", "retained"}, "resolve", map[string]string{"category": "reviewer", "environment": "host-b", "agent": "worker", "release": "retained"}},
		{[]string{"plan"}, "plan", nil}, {[]string{"status"}, "status", nil}, {[]string{"doctor"}, "doctor", nil}, {[]string{"releases"}, "releases", nil},
	} {
		t.Run(test.view+"/"+test.args[0], func(t *testing.T) {
			f := newFakeServer(t)
			f.on("GET /api/v1/extensions/"+test.view, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				for key, want := range test.query {
					if got := r.URL.Query().Get(key); got != want {
						t.Errorf("%s=%q want %q", key, got, want)
					}
				}
				_, _ = w.Write([]byte(`{"items":[],"more":false}`))
			})
			e := newTestEnv(t, f.srv.URL)
			if code := e.main(append([]string{"extensions"}, test.args...)...); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			if len(f.seen("POST /api/v1/ops")) != 0 {
				t.Fatal("read submitted an operation")
			}
		})
	}
}

func TestExtensionBindingReadsWholeAgentsFileAndKeepsIndependentCAS(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/extensions/state", protocol.ExtensionState{Revision: 7})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
		var env protocol.Envelope
		_ = json.Unmarshal(raw, &env)
		var edit protocol.ExtensionEditPayload
		_ = json.Unmarshal(env.Payload, &edit)
		if env.Operation != "extensions.binding_set" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || edit.Binding == nil || edit.Binding.Revision != 2 || edit.Binding.Agent != (protocol.AgentIdentity{ExecutionEnvironmentID: "host-a", LocalID: "worker"}) || edit.Binding.AgentsMD != "whole file\nsecond paragraph\n" {
			t.Errorf("incomplete binding update %+v %+v", env, edit)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	})
	file := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(file, []byte("whole file\nsecond paragraph\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, f.srv.URL)
	if code := e.main("extensions", "agent", "set", "worker", "--environment", "host-a", "--agents-md", file, "--data", `{"revision":2,"choices":[],"agents_md":"replaced"}`); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
}

func TestPublishRetryDoesNotReplanAnOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
		var env protocol.Envelope
		_ = json.Unmarshal(raw, &env)
		if *env.ExpectedControlRevision != 4 || env.RequestID != "req_00000000000000000000000001" {
			t.Error("retry changed control identity", env)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	})
	e := newTestEnv(t, f.srv.URL)
	args := []string{"extensions", "publish", "--request-id", "req_00000000000000000000000001", "--revision", "4"}
	if code := e.main(args...); code != exitUsage {
		t.Fatal("retry replanned current state", code, e.errOut.String())
	}
	if code := e.main(append(args, "--data", `{"release":{"id":"retained","composition_version":1,"catalog":{"revision":3,"resources":[],"categories":[],"agents":[]}},"manifest":{"id":"retained","manifest_sha256":"hash","manifest_size":1}}`)...); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	if len(f.seen("GET /api/v1/extensions/plan")) != 0 || len(f.seen("GET /api/v1/extensions/state")) != 0 {
		t.Fatal("retry read a newer plan")
	}
}
