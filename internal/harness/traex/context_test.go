package traex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBothWorkerRolesExposeOrdinaryIssueContext(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			workers, servers := fakeWorkers(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			called := false
			worker, err := workers.Start(ctx, harness.WorkerSpec{Role: role, Dir: "/w", Spec: json.RawMessage(`{}`), ReadContext: func(_ context.Context, query protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
				called = true
				if query.Kind != "questions" || query.After != "next-original" || query.Limit != 7 {
					t.Error("ordinary query changed", query)
				}
				return protocol.RunContext{RunID: "current_issue_run", Role: role, Materials: protocol.IssueMaterials{Kind: query.Kind, More: true, Next: "later-page"}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			server := <-servers
			var start ThreadStartParams
			_ = json.Unmarshal(server.params(methodThreadStart), &start)
			contextTool := false
			for _, tool := range start.DynamicTools {
				if tool.Name == harness.ReadContextTool {
					contextTool = true
				}
				if tool.Name == harness.ReadObservationTool {
					t.Error("ordinary context expanded legacy deployment observation")
				}
			}
			if !contextTool {
				t.Error("ordinary context tool missing", string(server.params(methodThreadStart)))
			}
			server.send(map[string]any{"id": 7, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "context", "tool": harness.ReadContextTool, "arguments": map[string]any{"kind": "questions", "after": "next-original", "limit": 7}}})
			answer := <-server.answers
			raw, _ := json.Marshal(answer)
			if !called || !strings.Contains(string(raw), "current_issue_run") || !strings.Contains(string(raw), "later-page") {
				t.Error("ordinary tool callback or paging result missing", string(raw))
			}
			cancel(context.Canceled)
			for range worker.Events() {
			}
			if _, err := worker.Wait(); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
