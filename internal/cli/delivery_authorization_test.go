package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestTaskCLIShowsDeliveryAuthorizationWithoutPuttingItInSpec(t *testing.T) {
	authority := &protocol.DeliveryExecutionAuthority{
		TaskID: "task_execution", SourceTaskID: "task_source", DutyRevision: 2,
		Candidate: protocol.IssueDeliveryCandidate{DeliveryID: "pr-original", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("b", 40)},
		Approval:  &protocol.ObservedDeliveryApproval{DecisionID: "dec_original", DecisionVersion: 2, DecisionStatus: "answered", Option: "approve", SourceMessageID: "msg_original", SourceRevision: 1},
		Valid:     false, InvalidReason: "authorization_not_current",
	}
	task := protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: authority.TaskID}, Spec: json.RawMessage(`{"goal":"Execute the exact authorized delivery"}`), DeliveryAuthorization: authority}
	for _, args := range [][]string{{"task", authority.TaskID}, {"task", authority.TaskID, "--json"}, {"task", "show", authority.TaskID, "--json"}} {
		f := newFakeServer(t)
		f.answer("GET /api/v1/tasks/"+authority.TaskID, task)
		e := newTestEnv(t, f.srv.URL)
		if code := e.main(args...); code != exitOK {
			t.Fatalf("%v: %d %s", args, code, e.errOut)
		}
		for _, want := range []string{authority.SourceTaskID, authority.InvalidReason, authority.Candidate.HeadSHA, authority.Approval.DecisionID, authority.Approval.SourceMessageID} {
			if !strings.Contains(e.out.String(), want) {
				t.Fatalf("%v dropped %q: %s", args, want, e.out)
			}
		}
		if len(args) > 2 {
			var decoded protocol.TaskDetail
			if err := json.Unmarshal(e.out.Bytes(), &decoded); err != nil || decoded.DeliveryAuthorization == nil || decoded.DeliveryAuthorization.Valid || strings.Contains(string(decoded.Spec), "delivery_authorization") {
				t.Fatalf("CLI changed authority or the frozen spec: %+v %v", decoded, err)
			}
		}
		if len(f.seen("POST /api/v1/ops")) != 0 {
			t.Fatal("read view performed a mutation")
		}
	}
}
