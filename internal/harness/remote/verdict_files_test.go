package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRemotePendingVerdictReconcilesBeforeNativeExecutor(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "still unknown", true: "confirmed"}[confirmed], func(t *testing.T) {
			manifest := protocol.ArtifactFile{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}
			spec := harness.WorkerSpec{Role: protocol.RoleAccept, RunID: "original-run", StateDir: t.TempDir(), AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					return nil, errors.New("upload receipt is unknown")
				}}
			v := harness.Verdicts{Spec: spec}
			if got := v.Handle(t.Context(), harness.ToolCall{ThreadID: "original-thread", TurnID: "original-turn", Tool: harness.VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{},"files":[{"path":"checks.log","name":"checks.log"}]}`)}); got.Success {
				t.Fatal("unknown original was acknowledged")
			}
			calls := 0
			spec.SealVerdictFiles = func(_ context.Context, binding string, raw json.RawMessage, files []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				calls++
				if !strings.Contains(binding, "original-thread") || !strings.Contains(binding, "original-turn") || len(files) != 1 || !strings.Contains(string(raw), `"verdict":"blocked"`) {
					t.Fatalf("remote recovery lost original authority: %q %s %+v", binding, raw, files)
				}
				if confirmed {
					return []protocol.ArtifactFile{manifest}, nil
				}
				return nil, errors.New("original receipt remains unknown")
			}
			spec.Lifetime = t.Context()
			spec.NativeDial = func(context.Context, string) (io.ReadWriteCloser, error) {
				t.Fatal("recovery opened another native transport")
				return nil, nil
			}
			workers := NewWorkers(WorkerOptions{Bin: "must-not-start"})
			defer workers.Close()
			w, err := workers.Start(t.Context(), spec)
			if calls != 1 {
				t.Fatalf("original sealing hook was not forwarded: %d", calls)
			}
			if !confirmed {
				if w != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
					t.Fatalf("unknown recovery escaped: %+v %v", w, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for range w.Events() {
			}
			result, err := w.Wait()
			if err != nil || result.Verdict == nil || !reflect.DeepEqual(result.Verdict.Files, []protocol.ArtifactFile{manifest}) {
				t.Fatalf("remote recovered manifest lost: %+v %v", result, err)
			}
		})
	}
}

func testVerdictFilesIdentity(context.Context, string, json.RawMessage) (string, error) {
	return strings.Repeat("b", 64), nil
}

func testValidateVerdictFiles(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) error {
	return nil
}
