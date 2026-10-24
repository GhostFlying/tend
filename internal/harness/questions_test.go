package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAskOwnerWaitsForOwnerForBothWorkerRoles(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			called, answer := make(chan struct{}), make(chan struct{})
			results := Results{Spec: WorkerSpec{Role: role, AskOwner: func(ctx context.Context, p protocol.AskOwnerPayload) (protocol.WorkerQuestion, error) {
				if p.QuestionKey != "scope" || p.Evidence != "investigated" {
					t.Errorf("question payload: %+v", p)
				}
				close(called)
				select {
				case <-answer:
				case <-ctx.Done():
					return protocol.WorkerQuestion{}, ctx.Err()
				}
				return protocol.WorkerQuestion{QuestionKey: p.QuestionKey, Status: "answered", Answer: json.RawMessage(`{"text":"keep existing behavior"}`)}, nil
			}}}
			done := make(chan ToolResult, 1)
			go func() {
				done <- results.Handle(t.Context(), ToolCall{Tool: AskOwnerTool, Arguments: json.RawMessage(`{"question_key":"scope","question":"What behavior?","evidence":"investigated"}`)})
			}()
			<-called
			select {
			case <-done:
				t.Fatal("question returned before owner answer")
			default:
			}
			if results.Result() != nil {
				t.Fatal("question completed the task")
			}
			close(answer)
			got := <-done
			if !got.Success || !strings.Contains(got.Text, "keep existing behavior") {
				t.Fatalf("answer: %+v", got)
			}
		})
	}
}

func TestQuestionRecoveryInBriefContinuationAndHandback(t *testing.T) {
	spec := WorkerSpec{Questions: []protocol.WorkerQuestion{{QuestionKey: "scope", Question: "Which scope?", Evidence: "Observed two implementations", Status: "open"}, {QuestionKey: "format", Status: "answered", Answer: json.RawMessage(`{"text":"preserve original format"}`)}}}
	_, brief := Brief(spec)
	for name, prompt := range map[string]string{"brief": brief, "continuation": Continuation(spec), "handback": HandBack(spec)} {
		for _, want := range []string{"scope", "Which scope?", "Observed two implementations", "preserve original format", "exactly its question_key"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s lost %q: %s", name, want, prompt)
			}
		}
	}
}
