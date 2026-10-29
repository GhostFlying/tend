package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueCLIOriginalEnvelopeDryRunAndReplay(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	input := `{"plan":{"title":"Owner intent","project":"default","intent":"Investigate","stages":[]}}`
	if code := te.main("issue", "create", "--data", input, "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(te.out.Bytes(), &original); err != nil || !protocol.ValidID(protocol.PrefixRequest, original.RequestID) || original.Operation != "issues.create" {
		t.Fatal(err, te.out.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("dry-run sent a request")
	}
	file := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(file, te.out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var received protocol.Envelope
		_ = json.Unmarshal(body, &received)
		originalHash, originalErr := protocol.PayloadHash(&original)
		receivedHash, receivedErr := protocol.PayloadHash(&received)
		if originalErr != nil || receivedErr != nil || received.RequestID != original.RequestID || receivedHash != originalHash || received.Reason != original.Reason || received.TraceID != original.TraceID {
			t.Error("replay rebuilt request")
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: received.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"issue_id":"wi_created"}`)}})
	})
	te.out.Reset()
	te.errOut.Reset()
	if code := te.main("issue", "replay", "--file", file); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("replay sent unexpected writes")
	}
}

func TestIssueCLIRecoveryRequiresOriginalControlAndHasNoImplicitReads(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	requestID := te.IDs.New(protocol.PrefixRequest)
	for _, args := range [][]string{
		{"issue", "pause", "wi_target", "--request-id", requestID},
		{"issue", "pause", "wi_target", "--request-id", requestID, "--revision", "3"},
		{"issue", "pause", "wi_target", "--revision", "0", "--epoch", "1"},
		{"issue", "pause", "wi_target", "--revision", "3", "--epoch", "0"},
	} {
		if code := te.main(args...); code != exitUsage {
			t.Fatalf("%v exit%d stderr%s", args, code, te.errOut.String())
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("recovery silently read new values or sent a request")
	}
	f.answer("POST /api/v1/ops", protocol.APIResponse{Operation: &protocol.Operation{RequestID: requestID, Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeStaleRevision, "changed")}, CurrentIssue: &protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "wi_target"}, Revision: 4}}})
	te.out.Reset()
	te.errOut.Reset()
	if code := te.main("issue", "pause", "wi_target", "--request-id", requestID, "--revision", "3", "--epoch", "1"); code != exitError {
		t.Fatal(code)
	}
	if len(f.seen("POST /api/v1/ops")) != 1 || len(f.seen("GET /api/v1/issues/wi_target")) != 0 || !strings.Contains(te.out.String(), "current_issue") || !strings.Contains(te.errOut.String(), "--revision 3 --epoch 1") {
		t.Fatal("stale control silently retried/refreshed or lost original values", te.out.String(), te.errOut.String())
	}
}

func TestIssueTaskCLIUsesOnlyCanonicalPrimaryProjection(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		t.Run(map[bool]string{true: "canonical", false: "historical-only"}[canonical], func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			task := protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: "task-target", ControlRevision: 7, PrimaryWorkItemID: "wi_legacy"}}
			if canonical {
				task.PrimaryIssueID = "wi_current"
			}
			f.answer("GET /api/v1/tasks/task-target", task)
			f.answer("GET /api/v1/issues/wi_current", protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "wi_current", AssignmentEpoch: 4}, Revision: 9}})
			code := te.main("issue", "update-task", "task-target", "--data", `{}`, "--dry-run")
			if !canonical {
				if code != exitUsage || len(f.seen("GET /api/v1/issues/wi_legacy")) != 0 || len(f.seen("POST /api/v1/ops")) != 0 {
					t.Fatal("historical primary became a current Issue", code, te.errOut.String())
				}
				return
			}
			if code != exitOK || len(f.seen("GET /api/v1/issues/wi_current")) != 1 || len(f.seen("GET /api/v1/issues/wi_legacy")) != 0 {
				t.Fatal(code, te.errOut.String())
			}
			var env protocol.Envelope
			if err := json.Unmarshal(te.out.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Epoch int64 `json:"expected_assignment_epoch"`
			}
			if err := json.Unmarshal(env.Payload, &payload); err != nil || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || payload.Epoch != 4 || env.Target.Type != "task" || env.Target.ID != "task-target" {
				t.Fatal(env, payload, err)
			}
		})
	}
}
