package traex

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAcceptanceEvidenceFilesUseOriginalNativeToolScope(t *testing.T) {
	ws, servers := fakeWorkers(t)
	manifest := protocol.ArtifactFile{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}
	spec := harness.WorkerSpec{Role: protocol.RoleAccept, StateDir: t.TempDir(), AcceptanceEvidenceFiles: true, VerdictFilesIdentity: testVerdictFilesIdentity,
		SealVerdictFiles: func(_ context.Context, binding string, input json.RawMessage, files []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			if !strings.Contains(binding, "th_w") || !strings.Contains(binding, "tu_w") || len(files) != 1 || files[0].Name != "checks.log" || !strings.Contains(string(input), `"verdict":"blocked"`) {
				t.Errorf("lost native verdict scope: %q %s %+v", binding, input, files)
			}
			return []protocol.ArtifactFile{manifest}, nil
		}}
	w, err := ws.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	var start ThreadStartParams
	_ = json.Unmarshal(f.params(methodThreadStart), &start)
	if len(start.DynamicTools) != 1 || !strings.Contains(string(start.DynamicTools[0].InputSchema), `"files"`) {
		t.Fatalf("negotiated tool schema did not reach native thread: %s", f.params(methodThreadStart))
	}
	f.send(map[string]any{"id": 5, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "original-call", "tool": harness.VerdictTool,
		"arguments": map[string]any{"verdict": "blocked", "evidence": map[string]string{"reason": "required prerequisite is absent"}, "files": []map[string]string{{"path": "checks.log", "name": "checks.log"}}}}})
	<-f.answers
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]any{"id": "tu_w", "status": "completed"}}})
	for range w.Events() {
	}
	result, err := w.Wait()
	if !errors.Is(err, harness.ErrRetainedResumePending) || result.Verdict == nil || !reflect.DeepEqual(result.Verdict.Files, []protocol.ArtifactFile{manifest}) {
		t.Fatalf("native tool lost confirmed evidence manifest: %+v %v", result, err)
	}
}

func TestAcceptancePendingEvidenceReconcilesWithoutStartingNativeTurn(t *testing.T) {
	for _, succeeds := range []bool{false, true} {
		t.Run(map[bool]string{false: "still unknown", true: "confirmed"}[succeeds], func(t *testing.T) {
			spec := harness.WorkerSpec{Role: protocol.RoleAccept, StateDir: t.TempDir(), AcceptanceEvidenceFiles: true, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					return nil, errors.New("original upload is unknown")
				}}
			v := harness.Verdicts{Spec: spec}
			if got := v.Handle(t.Context(), harness.ToolCall{ThreadID: "original-thread", TurnID: "original-turn", Tool: harness.VerdictTool, Arguments: json.RawMessage(`{"verdict":"passed","evidence":{}}`)}); got.Success {
				t.Fatal("unknown original submission was acknowledged")
			}
			spec.SealVerdictFiles = func(_ context.Context, binding string, _ json.RawMessage, _ []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				if !strings.Contains(binding, "original-thread") || !strings.Contains(binding, "original-turn") {
					t.Fatalf("recovery changed native binding: %q", binding)
				}
				if succeeds {
					return nil, nil
				}
				return nil, errors.New("original upload is still unknown")
			}
			ws := NewWorkers(WorkerOptions{})
			ws.start = func(context.Context, Options) (*Session, error) {
				t.Fatal("reconciliation started another native session")
				return nil, nil
			}
			w, err := ws.Start(t.Context(), spec)
			if w != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
				t.Fatalf("recovery fabricated original session closure: %+v %v", w, err)
			}
			if succeeds {
				if !strings.Contains(err.Error(), "closure is unconfirmed") {
					t.Fatal(err)
				}
				restored := harness.Verdicts{Spec: spec}
				if err := restored.RestoreContext(t.Context()); err != nil || restored.Verdict() == nil || restored.Verdict().Verdict != protocol.VerdictPassed {
					t.Fatalf("confirmed original verdict was lost while preserving process uncertainty: %+v %v", restored.Verdict(), err)
				}
			}
		})
	}
}

func TestAcceptancePendingEvidenceAtTurnEndCannotBecomeInconclusive(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "still unknown", true: "confirmed"}[confirmed], func(t *testing.T) {
			ws, servers := fakeWorkers(t)
			calls := 0
			spec := harness.WorkerSpec{Role: protocol.RoleAccept, StateDir: t.TempDir(), AcceptanceEvidenceFiles: true, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					calls++
					if confirmed && calls > 1 {
						return nil, nil
					}
					return nil, errors.New("original submission is unknown")
				}}
			w, err := ws.Start(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			f.send(map[string]any{"id": 5, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "original-call", "tool": harness.VerdictTool, "arguments": map[string]any{"verdict": "blocked", "evidence": map[string]string{"prerequisite": "absent"}}}})
			<-f.answers
			f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]any{"id": "tu_w", "status": "completed"}}})
			for range w.Events() {
			}
			result, err := w.Wait()
			if calls != 2 {
				t.Fatalf("original submission was not reconciled at turn end: %d", calls)
			}
			if !confirmed {
				if !errors.Is(err, harness.ErrRetainedResumePending) || result.Verdict != nil {
					t.Fatalf("unknown submission became terminal: %+v %v", result, err)
				}
			} else if !errors.Is(err, harness.ErrRetainedResumePending) || result.Verdict == nil || result.Verdict.Verdict != protocol.VerdictBlocked {
				t.Fatalf("confirmed original verdict lost: %+v %v", result, err)
			}
		})
	}
}

func testVerdictFilesIdentity(context.Context, string, json.RawMessage) (string, error) {
	return strings.Repeat("b", 64), nil
}
