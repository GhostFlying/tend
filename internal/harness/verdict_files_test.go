package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestVerdictFilesRequireNegotiatedCapability(t *testing.T) {
	for _, field := range []string{"files", "Files", "FILES"} {
		for _, files := range []string{`[]`, `null`, `[{"path":"check.log","name":"check.log"}]`} {
			v := Verdicts{StateDir: t.TempDir()}
			got := v.Handle(t.Context(), ToolCall{Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{},"` + field + `":` + files + `}`)})
			if got.Success || v.Verdict() != nil {
				t.Fatalf("unnegotiated %s %s were acknowledged: %+v", field, files, got)
			}
		}
	}
	if strings.Contains(string(VerdictToolSpec.InputSchema), `"files"`) {
		t.Fatal("legacy verdict tool advertises files")
	}
	if !strings.Contains(string(VerdictToolFor(WorkerSpec{AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles}).InputSchema), `"files"`) {
		t.Fatal("negotiated verdict tool omits files")
	}
}

func TestAcceptanceFileBriefExplainsScratchLifetimeOnlyWhenNegotiated(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		instructions, _ := Brief(WorkerSpec{Role: protocol.RoleAccept, AcceptanceEvidenceFiles: enabled})
		if strings.Contains(instructions, "Acceptance scratch is temporary and deleted after acknowledged final submission") != enabled {
			t.Fatalf("scratch lifetime explanation does not follow negotiated capability: %t %s", enabled, instructions)
		}
		if strings.Contains(instructions, "Tend seals those bytes") != enabled {
			t.Fatalf("unnegotiated file durability promise: %t %s", enabled, instructions)
		}
	}
}

func TestVerdictFilesSealBeforeAcknowledgmentAndRestore(t *testing.T) {
	for _, verdict := range []protocol.Verdict{protocol.VerdictPassed, protocol.VerdictChangesRequired, protocol.VerdictBlocked, protocol.VerdictInconclusive} {
		t.Run(string(verdict), func(t *testing.T) {
			dir := t.TempDir()
			manifest := protocol.ArtifactFile{FileID: "file_evidence", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}
			calls := 0
			var binding string
			var input json.RawMessage
			spec := WorkerSpec{AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity, StateDir: dir, SealVerdictFiles: func(_ context.Context, b string, raw json.RawMessage, files []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				calls++
				if _, err := os.Stat(filepath.Join(dir, "verdict.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("verdict existed before files were confirmed: %v", err)
				}
				binding, input = b, append(json.RawMessage(nil), raw...)
				if !reflect.DeepEqual(files, []VerdictFileSelection{{Path: "scratch/checks.log", Name: "checks.log"}}) {
					t.Fatalf("selection changed: %+v", files)
				}
				return []protocol.ArtifactFile{manifest}, nil
			}}
			v := Verdicts{Spec: spec, RequireBinding: true}
			v.Bind("original-native-send")
			call := ToolCall{Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"` + string(verdict) + `", "evidence":{"z":2,"a":1}, "files":[{"path":"scratch/checks.log","name":"checks.log"}]}`)}
			if got := v.Handle(t.Context(), call); !got.Success {
				t.Fatal(got.Text)
			}
			if calls != 1 || binding != "original-native-send" || !strings.Contains(string(input), `"evidence":{"a":1,"z":2}`) {
				t.Fatalf("lost original binding or canonical input: %d %q %s", calls, binding, input)
			}
			next := Verdicts{Spec: WorkerSpec{StateDir: dir}, RequireBinding: true}
			next.Bind(binding)
			if err := next.Restore(); err != nil {
				t.Fatal(err)
			}
			if got := next.Verdict(); got == nil || got.Verdict != verdict || !reflect.DeepEqual(got.Files, []protocol.ArtifactFile{manifest}) {
				t.Fatalf("acknowledged file manifest was lost: %+v", got)
			}
			next.Bind("different-native-send")
			if err := next.Restore(); err != nil || next.Verdict() != nil {
				t.Fatalf("another binding inherited verdict: %v %+v", err, next.Verdict())
			}
		})
	}
}

func TestVerdictFileFailureNeverAcknowledgesOrFallsBackToUpload(t *testing.T) {
	for _, test := range []struct {
		name  string
		files []protocol.ArtifactFile
		err   error
	}{
		{name: "upload unknown", err: errors.New("original upload receipt is unknown")},
		{name: "missing manifest"},
		{name: "wrong delivery name", files: []protocol.ArtifactFile{{FileID: "file_a", Name: "different.log", Size: 1, SHA256: strings.Repeat("a", 64)}}},
		{name: "invalid hash", files: []protocol.ArtifactFile{{FileID: "file_a", Name: "checks.log", Size: 1, SHA256: "invalid"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			v := Verdicts{Spec: WorkerSpec{StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity,
				UploadFile: func(context.Context, string, string) (protocol.ArtifactFile, error) {
					t.Fatal("verdict bypassed sealing")
					return protocol.ArtifactFile{}, nil
				},
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					return test.files, test.err
				},
			}}
			got := v.Handle(t.Context(), ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{},"files":[{"path":"checks.log","name":"checks.log"}]}`)})
			if got.Success || v.Verdict() != nil {
				t.Fatalf("unconfirmed file was acknowledged: %+v", got)
			}
			if _, err := os.Stat(filepath.Join(dir, "verdict.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unconfirmed file was persisted: %v", err)
			}
		})
	}
}

func TestVerdictFilesRejectCollisionsBeforeSealing(t *testing.T) {
	for _, files := range []string{
		`[{"path":"checks.log","name":"checks.log"},{"path":"./checks.log","name":"other.log"}]`,
		`[{"path":"checks.log","name":"same.log"},{"path":"other.log","name":"same.log"}]`,
		`[{"path":"checks.log","name":"checks.log","file_id":"pretend"}]`,
		`[{"path":"checks.log","name":""}]`,
		`[{"path":" ","name":"checks.log"}]`,
	} {
		v := Verdicts{Spec: WorkerSpec{AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity, Dir: t.TempDir(), StateDir: t.TempDir(), SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			t.Fatal("invalid selection reached sealing")
			return nil, nil
		}}}
		if got := v.Handle(t.Context(), ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"passed","evidence":{},"files":` + files + `}`)}); got.Success {
			t.Fatalf("invalid selection was acknowledged: %s", files)
		}
	}
}

func TestFeedbackBriefListsOnlyExplicitFileManifests(t *testing.T) {
	_, input := Brief(WorkerSpec{Feedback: &protocol.Feedback{AcceptanceID: "acceptance_original", Verdict: protocol.VerdictChangesRequired,
		Issues: json.RawMessage(`["fix the failure"]`), Evidence: json.RawMessage(`{"path":"/old/scratch/opaque.log"}`),
		Files: []protocol.ArtifactFile{{FileID: "file_log", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}},
	}})
	for _, want := range []string{"acceptance_original", "file_log", "checks.log", "download_file", "only the explicitly listed file IDs"} {
		if !strings.Contains(input, want) {
			t.Errorf("feedback omits %q: %s", want, input)
		}
	}
	if strings.Count(input, "file_id:") != 1 {
		t.Fatalf("opaque evidence path became a file reference: %s", input)
	}
}

func TestFeedbackBriefDiscoversIssueMaterialsWithContextTool(t *testing.T) {
	reads := 0
	_, input := Brief(WorkerSpec{
		ReadContext: func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
			reads++
			return protocol.RunContext{}, nil
		},
		Feedback: &protocol.Feedback{
			AcceptanceID: "acceptance_original", Verdict: protocol.VerdictChangesRequired,
			Issues:   json.RawMessage(`["fix the failure"]`),
			Evidence: json.RawMessage(`{"path":"/old/scratch/opaque.log"}`),
			Files:    []protocol.ArtifactFile{{FileID: "file_log", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}},
		},
	})
	for _, want := range []string{"acceptance_original", "file_log", "checks.log", "download_file", "read_context can discover other saved Issue materials", "Paths inside evidence JSON are observations, not downloadable file references"} {
		if !strings.Contains(input, want) {
			t.Errorf("feedback omits %q: %s", want, input)
		}
	}
	if strings.Contains(input, "only the explicitly listed file IDs") || strings.Count(input, "file_id:") != 1 || reads != 0 {
		t.Fatalf("feedback restricted ordinary reading, promoted an opaque path, or performed a read: %s; reads=%d", input, reads)
	}
}

func TestPendingVerdictRestoresOriginalInputBeforeAnotherSubmission(t *testing.T) {
	dir := t.TempDir()
	manifest := protocol.ArtifactFile{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}
	input := json.RawMessage(`{"files":[{"name":"checks.log","path":"scratch/checks.log"}],"evidence":{"z":2,"a":1},"verdict":"blocked"}`)
	calls := 0
	spec := WorkerSpec{StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity, SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
		calls++
		return nil, errors.New("upload receipt is unknown")
	}}
	v := Verdicts{Spec: spec, RequireBinding: true}
	v.Bind("original-send")
	if got := v.Handle(t.Context(), ToolCall{Tool: VerdictTool, Arguments: input}); got.Success {
		t.Fatal("unknown upload was acknowledged")
	}
	originalPending, err := os.ReadFile(filepath.Join(dir, "pending-verdict.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"different selection", "different verdict", "no files", "new binding", "capability absent"} {
		t.Run(change, func(t *testing.T) {
			next := Verdicts{Spec: spec, RequireBinding: true}
			next.Bind("original-send")
			args := input
			switch change {
			case "different selection":
				args = json.RawMessage(strings.ReplaceAll(string(input), "scratch/checks.log", "changed.log"))
			case "different verdict":
				args = json.RawMessage(strings.ReplaceAll(string(input), "blocked", "passed"))
			case "no files":
				args = json.RawMessage(`{"verdict":"passed","evidence":{}}`)
			case "new binding":
				next.Bind("new-send")
			case "capability absent":
				next.Spec.AcceptanceEvidenceFiles = false
				args = json.RawMessage(`{"verdict":"passed","evidence":{}}`)
			}
			if got := next.Handle(t.Context(), ToolCall{Tool: VerdictTool, Arguments: args}); got.Success || calls != 1 {
				t.Fatalf("pending original submission was bypassed: %+v calls=%d", got, calls)
			}
			if change == "new binding" || change == "capability absent" {
				if err := next.RestoreContext(t.Context()); !errors.Is(err, ErrRetainedResumePending) {
					t.Fatalf("startup discarded pending original: %v", err)
				}
			}
			current, err := os.ReadFile(filepath.Join(dir, "pending-verdict.json"))
			if err != nil || string(current) != string(originalPending) {
				t.Fatalf("pending original was rewritten: %v %s", err, current)
			}
		})
	}
	next := Verdicts{Spec: spec, RequireBinding: true}
	next.Bind("original-send")
	next.Spec.SealVerdictFiles = func(_ context.Context, binding string, raw json.RawMessage, selected []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
		calls++
		if binding != "original-send" || !strings.Contains(string(raw), `"evidence":{"a":1,"z":2}`) || selected[0].Path != "scratch/checks.log" {
			t.Fatalf("recovery changed original input: %q %s %+v", binding, raw, selected)
		}
		return []protocol.ArtifactFile{manifest}, nil
	}
	if err := next.RestoreContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || next.Verdict() == nil || !reflect.DeepEqual(next.Verdict().Files, []protocol.ArtifactFile{manifest}) {
		t.Fatalf("original submission did not reconcile: calls=%d verdict=%+v", calls, next.Verdict())
	}
	if _, err := os.Stat(filepath.Join(dir, "pending-verdict.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed original remained pending: %v", err)
	}
}

func TestNegotiatedNoFileVerdictsStillPinOriginalInput(t *testing.T) {
	for _, fields := range []string{"", `,"files":[]`} {
		calls := 0
		v := Verdicts{Spec: WorkerSpec{StateDir: t.TempDir(), AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity, SealVerdictFiles: func(_ context.Context, binding string, input json.RawMessage, files []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			calls++
			if binding == "" || len(files) != 0 || !strings.Contains(string(input), `"evidence":{}`) {
				t.Fatalf("no-file input lost identity: %q %s %+v", binding, input, files)
			}
			return nil, nil
		}}}
		if got := v.Handle(t.Context(), ToolCall{ThreadID: "original-thread", TurnID: "original-turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"passed","evidence":{}` + fields + `}`)}); !got.Success || calls != 1 {
			t.Fatalf("no-file negotiated verdict did not pin original: %+v calls=%d", got, calls)
		}
	}
}

func TestVerdictFilePersistenceFailureRetainsRecoveryIntent(t *testing.T) {
	dir := t.TempDir()
	first := true
	v := Verdicts{Spec: WorkerSpec{StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity, SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
		if first {
			first = false
			if err := os.Mkdir(filepath.Join(dir, "verdict.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return nil, nil
	}}}
	got := v.Handle(t.Context(), ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"passed","evidence":{}}`)})
	if got.Success || v.Verdict() != nil {
		t.Fatal("failed final persistence was acknowledged")
	}
	if _, err := os.Stat(filepath.Join(dir, "pending-verdict.json")); err != nil {
		t.Fatalf("failed final persistence discarded original intent: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "verdict.json")); err != nil {
		t.Fatal(err)
	}
	if err := v.RestoreContext(t.Context()); err != nil || v.Verdict() == nil {
		t.Fatalf("original intent did not recover: %v %+v", err, v.Verdict())
	}
}

func testVerdictFilesIdentity(context.Context, string, json.RawMessage) (string, error) {
	return strings.Repeat("b", 64), nil
}

func TestSealedVerdictRecoveryChecksIdentityBeforeUpload(t *testing.T) {
	for _, change := range []string{"native binding", "input", "sealed identity", "unreadable sealed identity"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			uploads := 0
			var originalBinding string
			var originalInput json.RawMessage
			spec := WorkerSpec{StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(_ context.Context, binding string, input json.RawMessage, _ []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					uploads++
					originalBinding, originalInput = binding, append(json.RawMessage(nil), input...)
					return nil, errors.New("original upload receipt is unknown")
				}}
			v := Verdicts{Spec: spec}
			if got := v.Handle(t.Context(), ToolCall{ThreadID: "original-thread", TurnID: "original-turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{},"files":[{"path":"check.log","name":"check.log"}]}`)}); got.Success {
				t.Fatal("unknown original was acknowledged")
			}
			path := filepath.Join(dir, "pending-verdict.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var pending pendingVerdict
			if err := json.Unmarshal(raw, &pending); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "native binding":
				pending.NativeBinding = "another-native-binding"
			case "input":
				pending.Input = json.RawMessage(strings.ReplaceAll(string(pending.Input), "blocked", "passed"))
			case "sealed identity":
				pending.SealedIdentity = strings.Repeat("c", 64)
			}
			raw, err = json.Marshal(pending)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			spec.VerdictFilesIdentity = func(_ context.Context, binding string, input json.RawMessage) (string, error) {
				if change == "unreadable sealed identity" || binding != originalBinding || string(input) != string(originalInput) {
					return "", errors.New("original sealed input identity is unavailable")
				}
				return strings.Repeat("b", 64), nil
			}
			uploads = 0
			next := Verdicts{Spec: spec}
			if err := next.RestoreContext(t.Context()); !errors.Is(err, ErrRetainedResumePending) {
				t.Fatalf("changed original input escaped reconciliation: %v", err)
			}
			if uploads != 0 {
				t.Fatalf("changed sealed input reached the upload hook: %d", uploads)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatalf("rejected pending identity was rewritten: %v", err)
			}
		})
	}
}

func TestDefiniteVerdictSelectionRejectionAllowsCorrection(t *testing.T) {
	for _, kind := range []string{"invalid basename", "definite missing path", "unknown missing path", "already sealed", "prior unknown preflight"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			uploads, intent := 0, false
			spec := WorkerSpec{StateDir: dir, Dir: t.TempDir(), AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles,
				VerdictFilesIdentity: func(context.Context, string, json.RawMessage) (string, error) {
					if !intent {
						return "", errors.New("no intent was created")
					}
					return strings.Repeat("b", 64), nil
				},
				SealVerdictFiles: func(_ context.Context, _ string, _ json.RawMessage, files []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					uploads++
					if files[0].Path == "missing.log" {
						if kind == "definite missing path" {
							return nil, errors.Join(ErrVerdictSelectionRejected, errors.New("file does not exist"))
						}
						intent = kind == "already sealed"
						return nil, errors.New("selection or upload is unknown")
					}
					intent = true
					return []protocol.ArtifactFile{{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}}, nil
				}}
			v := Verdicts{Spec: spec}
			call := func(path, name string) ToolCall {
				return ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{},"files":[{"path":"` + path + `","name":"` + name + `"}]}`)}
			}
			invalid := call("missing.log", "checks.log")
			if kind == "invalid basename" {
				invalid = call("actual.log", "logs/checks.log")
			}
			if got := v.Handle(t.Context(), invalid); got.Success {
				t.Fatal("invalid selection was acknowledged")
			}
			if kind == "already sealed" || kind == "prior unknown preflight" {
				v.Spec.SealVerdictFiles = func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					uploads++
					return nil, ErrVerdictSelectionRejected
				}
				if got := v.Handle(t.Context(), invalid); got.Success {
					t.Fatal("contradictory sealed rejection was acknowledged")
				}
			}
			before := uploads
			got := v.Handle(t.Context(), call("actual.log", "checks.log"))
			correctable := kind == "invalid basename" || kind == "definite missing path"
			if got.Success != correctable {
				t.Fatalf("correction classification differs: %+v", got)
			}
			if !correctable && uploads != before {
				t.Fatal("uncertain or sealed input was replaced")
			}
			facts, err := filepath.Glob(filepath.Join(dir, "verdict-rejections", "*.json"))
			if err != nil {
				t.Fatal(err)
			}
			if correctable && len(facts) != 1 {
				t.Fatalf("definite rejection fact was lost: %v", facts)
			}
			if !correctable && len(facts) != 0 {
				t.Fatal("unknown effect was recorded as a definite rejection")
			}
		})
	}
}

func testValidateVerdictFiles(context.Context, string, json.RawMessage, []VerdictFileSelection) error {
	return nil
}
