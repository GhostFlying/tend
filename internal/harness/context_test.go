package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestReadContextIsOrdinaryForImplementAcceptAndSuccessor(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		for _, attempt := range []int{1, 2} {
			spec := WorkerSpec{Role: role, Attempt: attempt, Spec: json.RawMessage(`{}`)}
			var received protocol.IssueMaterialsQuery
			spec.ReadContext = func(_ context.Context, query protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
				received = query
				return protocol.RunContext{RunID: "current_run", TaskID: "original_task", Role: role, Materials: protocol.IssueMaterials{Kind: "files", ObservedAt: 17, Seq: 23, More: true, Next: "next-page", Files: []protocol.IssueMaterialFile{{ArtifactFile: protocol.ArtifactFile{FileID: "file_saved", SHA256: "original-hash"}, Sources: []protocol.IssueMaterialSource{{Kind: "background", Status: "withdrawn", ContentRevision: 4}}}}}}, nil
			}
			r := Results{Spec: spec}
			got := r.Handle(t.Context(), ToolCall{Tool: "read_context", Arguments: json.RawMessage(`{"issue_id":"issue_related","kind":"files","id":"file_saved","after":"opaque-cursor","limit":7}`)})
			want := protocol.IssueMaterialsQuery{IssueID: "issue_related", Kind: "files", ID: "file_saved", After: "opaque-cursor", Limit: 7}
			var decoded protocol.RunContext
			if !got.Success || !reflect.DeepEqual(received, want) || json.Unmarshal([]byte(got.Text), &decoded) != nil || decoded.Materials.Next != "next-page" || !decoded.Materials.More || decoded.Materials.Files[0].Sources[0].Status != "withdrawn" {
				t.Fatal("ordinary context or retained provenance was lost", role, attempt, got, received)
			}
			instructions, input := Brief(spec)
			if !strings.Contains(instructions+input, "read_context") || !strings.Contains(instructions+input, "starting snapshot") || !strings.Contains(instructions+input, "not a reading allowlist") {
				t.Fatal("worker is still told to treat frozen inputs as its reading ACL", instructions, input)
			}
		}
	}
}

func TestReadContextArgumentsAndUnavailableResponses(t *testing.T) {
	calls := 0
	r := Results{Spec: WorkerSpec{Spec: json.RawMessage(`{}`), ReadContext: func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
		calls++
		return protocol.RunContext{}, errors.New("secret bearer credential")
	}}}
	for _, raw := range []string{`null`, `[]`, `{"limit":"bad"}`, `{"unexpected":"ignored"}`} {
		if got := r.Handle(t.Context(), ToolCall{Tool: "read_context", Arguments: json.RawMessage(raw)}); got.Success || calls != 0 {
			t.Fatal("invalid query was silently interpreted as another read", raw, got, calls)
		}
	}
	if got := r.Handle(t.Context(), ToolCall{Tool: "read_context", Arguments: json.RawMessage(`{}`)}); got.Success || strings.Contains(got.Text, "secret") || strings.Contains(got.Text, "credential") || calls != 1 {
		t.Fatal("unavailable read became success or leaked transport detail", got, calls)
	}
	r.Spec.ReadContext = nil
	if got := r.Handle(t.Context(), ToolCall{Tool: "read_context", Arguments: json.RawMessage(`{}`)}); got.Success || calls != 1 {
		t.Fatal("missing reader was reported as successful context", got, calls)
	}
}

func TestReadContextHandBackRecognizesPriorSavedInput(t *testing.T) {
	spec := WorkerSpec{Role: protocol.RoleAccept, Spec: json.RawMessage(`{}`)}
	previous := HandBack(spec)
	spec.ReadContext = func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
		return protocol.RunContext{}, nil
	}
	if !IsHandBack(spec, previous) || !IsHandBack(spec, HandBack(spec)) || !strings.Contains(Continuation(spec), "read_context") {
		t.Fatal("new ordinary tool invalidated a retained handback or disappeared from continuation")
	}
}
