package traex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedExecutionReachesNativeThreadAndTurn(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			ws, servers := fakeWorkers(t)
			profile := &protocol.ExecutionProfile{Model: "named-model", Effort: "xhigh"}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			w, err := ws.Start(ctx, harness.WorkerSpec{Role: role, Dir: "/w", Spec: json.RawMessage(`{"goal":"work","acceptance":["check"]}`), FrozenExecution: profile, RoleInstructions: "PRIVATE_NAMED_INSTRUCTIONS"})
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			var thread ThreadStartParams
			var turn TurnStartParams
			json.Unmarshal(f.params(methodThreadStart), &thread)
			json.Unmarshal(f.params(methodTurnStart), &turn)
			if thread.Model != profile.Model || turn.Model != profile.Model || turn.Effort != profile.Effort || !strings.Contains(thread.DeveloperInstructions, "PRIVATE_NAMED_INSTRUCTIONS") {
				t.Fatal("native request ignored role configuration", thread, turn)
			}
			if role == protocol.RoleAccept && (!strings.Contains(thread.DeveloperInstructions, "independent reviewer") || thread.DynamicTools[0].Name != harness.VerdictTool) {
				t.Fatal("named prompt replaced formal acceptance contract")
			}
			cancel(context.Canceled)
			for range w.Events() {
			}
			_, _ = w.Wait()
		})
	}
}

func TestNamedExecutionResumeKeepsRequestedThreadModelEffortAndInstructions(t *testing.T) {
	ws, servers := fakeWorkers(t, func(f *fakeServer) {
		f.on(methodThreadResume, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_original"}} })
	})
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	profile := &protocol.ExecutionProfile{Model: "original-model", Effort: "high"}
	w, err := ws.Start(ctx, harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Attempt: 2, Thread: "th_original", Spec: json.RawMessage(`{"goal":"work"}`), FrozenExecution: profile, RoleInstructions: "ORIGINAL_PRIVATE_ROLE"})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	var resume ThreadResumeParams
	var turn TurnStartParams
	if err := json.Unmarshal(f.params(methodThreadResume), &resume); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.params(methodTurnStart), &turn); err != nil {
		t.Fatal(err)
	}
	if resume.ThreadID != "th_original" || resume.Model != profile.Model || turn.Model != profile.Model || turn.Effort != profile.Effort || !strings.Contains(resume.DeveloperInstructions, "ORIGINAL_PRIVATE_ROLE") || f.params(methodThreadStart) != nil {
		t.Fatal("resume changed frozen request", resume, turn)
	}
	cancel(context.Canceled)
	for range w.Events() {
	}
	_, _ = w.Wait()
}
