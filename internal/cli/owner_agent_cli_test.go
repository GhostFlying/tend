package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueAssignmentCLIUsesCurrentCASAndOriginalRequestOnStale(t *testing.T) {
	for _, action := range []string{"assign", "finish-assignment"} {
		t.Run(action, func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			f.answer("GET /api/v1/issues/wi_target", protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "wi_target", AssignmentEpoch: 3}, Revision: 7}})
			body := `{"reason":"Owner transfers responsibility","assignee":{"kind":"owner","ref":"owner"}}`
			operation := "issues.assign"
			if action == "finish-assignment" {
				body = `{"reason":"Original control reconciled","handoff_id":"handoff-original"}`
				operation = "issues.finish_assignment"
			}
			var original protocol.Envelope
			calls := 0
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
				var env protocol.Envelope
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Error(err)
				}
				var payload struct {
					Epoch int64 `json:"expected_assignment_epoch"`
				}
				if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Epoch != 3 || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || env.Target.Type != "issue" || env.Target.ID != "wi_target" || env.Operation != operation || !strings.Contains(te.errOut.String(), env.RequestID) {
					t.Error("incorrect assignment envelope or request not visible before HTTP", env, payload, err, te.errOut.String())
				}
				calls++
				if calls == 1 {
					original = env
					_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
					return
				}
				first, _ := protocol.PayloadHash(&original)
				second, _ := protocol.PayloadHash(&env)
				if env.RequestID != original.RequestID || first != second {
					t.Error("retry replaced original assignment request", env)
				}
				_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeStaleRevision, "assignment changed")}, CurrentIssue: &protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "wi_target", AssignmentEpoch: 4}, Revision: 8}}})
			})
			if code := te.main("issue", action, "wi_target", "--data", body); code != exitOK {
				t.Fatal(code, te.errOut.String())
			}
			if len(f.seen("GET /api/v1/issues/wi_target")) != 1 || !protocol.ValidID(protocol.PrefixRequest, original.RequestID) {
				t.Fatal("first request did not use current control", original)
			}
			te.errOut.Reset()
			te.out.Reset()
			if code := te.main("issue", action, "wi_target", "--data", body, "--request-id", original.RequestID, "--revision", "7", "--epoch", "3"); code != exitError {
				t.Fatal(code, te.errOut.String())
			}
			if calls != 2 || len(f.seen("GET /api/v1/issues/wi_target")) != 1 || !strings.Contains(te.out.String(), "current_issue") {
				t.Fatal("stale assignment silently refreshed or lost current Issue", calls, te.out.String())
			}
		})
	}
}

func TestHostedAgentCatalogCLIReadsExistingProjectionAndErrors(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/hosted-agents", protocol.HostedAgentInventory{ConfigurationRevision: 4, MainAgentID: "planner", State: "registered_staged"})
	if code := te.main("agents", "hosted"); code != exitOK || !strings.Contains(te.out.String(), `"configuration_revision":4`) || !strings.Contains(te.out.String(), `"state":"registered_staged"`) {
		t.Fatal(code, te.out.String(), te.errOut.String())
	}
	f.on("GET /api/v1/hosted-agents", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: protocol.Errorf(protocol.CodeForbidden, "owner access required")})
	})
	te.out.Reset()
	te.errOut.Reset()
	if code := te.main("agents", "hosted"); code != exitError || te.out.Len() != 0 || !strings.Contains(te.errOut.String(), "forbidden") {
		t.Fatal(code, te.out.String(), te.errOut.String())
	}
	if len(f.seen("GET /api/v1/hosted-agents")) != 2 || len(f.seen("POST /api/v1/ops")) != 0 || !strings.Contains(usage, "issue finish-assignment") || !strings.Contains(usage, "agents hosted") {
		t.Fatal("read command mutated state or help omitted entry")
	}
}
