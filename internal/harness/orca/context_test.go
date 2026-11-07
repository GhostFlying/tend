package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOrcaBothWorkerRolesReadOrdinaryIssueContextThroughBridge(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			fixture := newWorkerFixture(t)
			spec := fixture.spec(role)
			spec.Spec = json.RawMessage(`{}`)
			called := false
			spec.ReadContext = func(_ context.Context, query protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
				called = true
				if query.Kind != "files" || query.After != "next-original" || query.Limit != 7 {
					t.Error("ordinary query changed", query)
				}
				return protocol.RunContext{RunID: "current_issue_run", Role: role, Materials: protocol.IssueMaterials{Kind: query.Kind, More: true, Next: "later-page"}}, nil
			}
			worker, _, drained := fixture.start(t.Context(), spec)
			state, err := bridge.StatePath(t.Context(), fixture.dir)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_context","arguments":{"kind":"files","after":"next-original","limit":7}}}` + "\n"
			if err := bridge.ServeMCP(t.Context(), state, spec.RunID, strings.NewReader(input), &out); err != nil || strings.Contains(out.String(), "isError") || !strings.Contains(out.String(), `"name":"read_context"`) || !strings.Contains(out.String(), "current_issue_run") || !strings.Contains(out.String(), "later-page") || !called {
				t.Error("ordinary context bridge failed", out.String(), err)
			}
			if strings.Contains(out.String(), `"name":"read_observation"`) {
				t.Error("ordinary context expanded legacy deployment observation")
			}
			if role == protocol.RoleAccept {
				fixture.submit(protocol.VerdictPassed)
			}
			fixture.orca.finish("term_1", "read current Issue context")
			if _, err := worker.Wait(); err != nil {
				t.Fatal(err)
			}
			<-drained
		})
	}
}
