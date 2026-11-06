package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedAcceptanceReconcilesOriginalEvidenceSubmission(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "still unknown", true: "confirmed"}[confirmed], func(t *testing.T) {
			f, original, a := nativeFixture(t)
			a.RootTurn.State, a.RootTurn.Operations = "completed", "settled"
			a.RootTurn.ErrorCode, a.RootTurn.Retryable = "", false
			a.MainAgent.Outcome = ""
			if _, _, err := original.agent(t.Context()); err != nil {
				t.Fatal(err)
			}
			original.st.Worktree, original.st.SessionID = original.wt, a.ProviderSession.ID
			original.spec.Role, original.spec.StateDir = protocol.RoleAccept, t.TempDir()
			original.spec.AcceptanceEvidenceFiles = true
			original.spec.ValidateVerdictFiles = testValidateVerdictFiles
			original.spec.VerdictFilesIdentity = testVerdictFilesIdentity
			original.spec.SealVerdictFiles = func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				return nil, errors.New("original upload is unknown")
			}
			if err := original.openBridge(t.Context()); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"verdict":"blocked","evidence":{"prerequisite":"absent"},"files":[{"path":"checks.log","name":"checks.log"}]}}}` + "\n"
			if err := bridge.ServeMCP(t.Context(), filepath.Join(original.spec.StateDir, protocol.WorkerMarker), original.spec.RunID, strings.NewReader(input), &output); err != nil || !strings.Contains(output.String(), "isError") {
				t.Fatalf("unknown original upload was acknowledged: %s %v", output.String(), err)
			}
			original.closeBridge(t.Context())
			manifest := protocol.ArtifactFile{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}
			spec := original.spec
			spec.Attempt, spec.Thread = 2, mustState(original.st)
			spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "original-retained-authority", Mode: "attach"}
			spec.OnThread = func(string) error { return nil }
			spec.SealVerdictFiles = func(_ context.Context, binding string, raw json.RawMessage, files []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				if !strings.Contains(binding, original.spec.RunID) || !strings.Contains(string(raw), `"verdict":"blocked"`) || len(files) != 1 || files[0].Name != "checks.log" {
					t.Fatalf("lost original native evidence scope: %q %s %+v", binding, raw, files)
				}
				if confirmed {
					return []protocol.ArtifactFile{manifest}, nil
				}
				return nil, errors.New("original upload remains unknown")
			}
			rebuilt, err := f.ws.Start(t.Context(), spec)
			if !confirmed {
				if rebuilt != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
					t.Fatalf("unknown original escaped startup: %+v %v", rebuilt, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for range rebuilt.Events() {
				}
				result, err := rebuilt.Wait()
				if err != nil || result.Verdict == nil || result.Verdict.Verdict != protocol.VerdictBlocked || !reflect.DeepEqual(result.Verdict.Files, []protocol.ArtifactFile{manifest}) {
					t.Fatalf("confirmed original evidence was lost: %+v %v", result, err)
				}
			}
			if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("evidence recovery mutated original native execution")
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
