package cli

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"reflect"
	"testing"
)

func TestCapabilityAgentIssueReadFiltersAndCompleteOutput(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	want := protocol.AgentIssuePage{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner-one", LocalID: "cedar"}, Coverage: "recorded_run_pins", Items: []protocol.AgentIssue{{IssueID: "wi_00000000000000000000000001", State: protocol.IssueActive, Current: true, Runs: []protocol.AgentIssueRun{{TaskID: "task", Run: protocol.RunView{RunID: "original", Observed: protocol.RunUnknown}, Pin: protocol.RunExecutionPin{AgentRevision: 3}, Source: "run_responsibility", PendingDecision: true}}}}, More: true, Next: "wi_00000000000000000000000002"}
	f.on("GET /api/v1/agent-issues", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		for k, v := range map[string]string{"environment_id": "runner-one", "local_id": "cedar", "scope": "history", "state": "closed", "search": "two words", "after": "wi_00000000000000000000000001", "limit": "7", "snapshot_id": "original-snapshot", "agent_revision": "3"} {
			if q.Get(k) != v {
				t.Errorf("%s=%q", k, q.Get(k))
			}
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	if code := te.main("agents", "issues", "--environment", "runner-one", "--agent", "cedar", "--scope", "history", "--state", "closed", "--search", "two words", "--after", "wi_00000000000000000000000001", "--limit", "7", "--snapshot", "original-snapshot", "--agent-revision", "3", "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var got protocol.AgentIssuePage
	if err := json.Unmarshal(te.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("output=%+v %v", got, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("read created business write")
	}
	for _, args := range [][]string{{"agents", "issues"}, {"agents", "issues", "--environment", "runner-one", "--agent", "cedar", "--scope", "running"}, {"agents", "issues", "--environment", "runner-one", "--agent", "cedar", "--limit", "101"}} {
		env := newTestEnv(t, f.srv.URL)
		if code := env.main(args...); code != exitUsage {
			t.Fatalf("%v=%d", args, code)
		}
	}
}
