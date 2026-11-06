package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAcknowledgedFileVerdictReplayPreservesOriginalState(t *testing.T) {
	for _, tc := range []struct {
		name, old, next                                                    string
		reopen, differentNative, differentRecord, identityUnknown, noClose bool
		success                                                            bool
	}{
		{name: "same no files", success: true},
		{name: "same before close", noClose: true, success: true},
		{name: "same selected files", old: `,"files":[{"path":"checks.log","name":"checks.log"}]`, success: true},
		{name: "same after reopen", reopen: true, success: true},
		{name: "semantic JSON replay", next: `{"issues":["repair"],"evidence":{"a":1,"z":2},"verdict":"blocked"}`, success: true},
		{name: "changed evidence", next: `{"verdict":"blocked","evidence":{"z":3,"a":1},"issues":["repair"]}`},
		{name: "changed issues", next: `{"verdict":"blocked","evidence":{"z":2,"a":1},"issues":["other repair"]}`},
		{name: "changed verdict", next: `{"verdict":"passed","evidence":{"z":2,"a":1},"issues":["repair"]}`},
		{name: "changed selected path", old: `,"files":[{"path":"checks.log","name":"checks.log"}]`, next: `{"verdict":"blocked","evidence":{"z":2,"a":1},"issues":["repair"],"files":[{"path":"another.log","name":"checks.log"}]}`},
		{name: "changed selected name", old: `,"files":[{"path":"checks.log","name":"checks.log"}]`, next: `{"verdict":"blocked","evidence":{"z":2,"a":1},"issues":["repair"],"files":[{"path":"checks.log","name":"another.log"}]}`},
		{name: "invalid selected name after ACK", next: `{"verdict":"blocked","evidence":{"z":2,"a":1},"issues":["repair"],"files":[{"path":"checks.log","name":"logs/checks.log"}]}`},
		{name: "explicit empty files differs", next: `{"verdict":"blocked","evidence":{"z":2,"a":1},"issues":["repair"],"files":[]}`},
		{name: "changed native binding", differentNative: true},
		{name: "changed record binding", differentRecord: true},
		{name: "changed after reopen", reopen: true, next: `{"verdict":"blocked","evidence":{"check":"updated"}}`},
		{name: "owner identity IO unknown", identityUnknown: true},
		{name: "owner identity IO unknown after reopen", reopen: true, identityUnknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var sealedBinding string
			var sealedInput json.RawMessage
			calls, uploads, ids := 0, 0, 0
			unknown := false
			spec := WorkerSpec{RunID: "run_original", Role: protocol.RoleAccept, StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles}
			spec.SealVerdictFiles = func(_ context.Context, binding string, input json.RawMessage, selections []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				calls++
				ids++
				sealedBinding, sealedInput = binding, append(json.RawMessage(nil), input...)
				if err := os.WriteFile(filepath.Join(dir, "owner-intent.json"), input, 0600); err != nil {
					t.Fatal(err)
				}
				var files []protocol.ArtifactFile
				for _, selected := range selections {
					uploads++
					files = append(files, protocol.ArtifactFile{FileID: "file_original", Name: selected.Name, Size: 7, SHA256: strings.Repeat("a", 64)})
				}
				return files, nil
			}
			spec.VerdictFilesIdentity = func(_ context.Context, binding string, input json.RawMessage) (string, error) {
				if unknown || binding != sealedBinding || !bytes.Equal(input, sealedInput) {
					return "", errors.New("original owner identity unavailable")
				}
				return strings.Repeat("b", 64), nil
			}
			v := &Verdicts{Spec: spec}
			call := ToolCall{ThreadID: "thread_original", TurnID: "turn_original", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{"z":2,"a":1},"issues":["repair"]` + tc.old + `}`)}
			if got := v.Handle(t.Context(), call); !got.Success {
				t.Fatal(got.Text)
			}
			original := *v.Verdict()
			// This component fixture supplies the close fact; native process ownership
			// and actual Close remain covered by the direct-session integration cases.
			if !tc.noClose {
				if err := v.RecordOwnedSessionClose(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reopen {
				v = &Verdicts{Spec: spec}
			}
			if tc.next != "" {
				call.Arguments = json.RawMessage(tc.next)
			}
			if tc.differentNative {
				call.TurnID = "turn_other"
			}
			if tc.differentRecord {
				v.Bind("record_other")
			}
			unknown = tc.identityUnknown
			before := snapshotVerdictState(t, dir)
			oldCalls, oldUploads, oldIDs := calls, uploads, ids
			got := v.Handle(t.Context(), call)
			if got.Success != tc.success {
				t.Fatalf("replay success=%v want=%v: %s", got.Success, tc.success, got.Text)
			}
			if calls != oldCalls || uploads != oldUploads || ids != oldIDs {
				t.Fatalf("replay created effects: seal=%d upload=%d ID=%d", calls-oldCalls, uploads-oldUploads, ids-oldIDs)
			}
			if after := snapshotVerdictState(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatalf("replay changed durable state:\nbefore=%+v\nafter=%+v", before, after)
			}
			unknown = false
			restored := Verdicts{Spec: spec}
			if err := restored.RestoreContext(t.Context()); err != nil {
				t.Fatalf("original ACK no longer restores: %v", err)
			}
			if !reflect.DeepEqual(restored.Verdict(), &original) {
				t.Fatalf("restored result changed: %+v", restored.Verdict())
			}
			if !tc.noClose {
				if err := restored.ConfirmOwnedSessionClose(); err != nil {
					t.Fatalf("original close no longer confirms: %v", err)
				}
			}
			if tc.success && !reflect.DeepEqual(v.Verdict(), &original) {
				t.Fatalf("exact replay did not return original payload: %+v", v.Verdict())
			}
		})
	}
}

func TestUnknownPendingVerdictRejectsChangedCanonicalInput(t *testing.T) {
	for _, change := range []string{"evidence", "issues", "path", "name", "binding"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			var originalBinding string
			var originalInput json.RawMessage
			spec := WorkerSpec{RunID: "run_original", StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles}
			spec.SealVerdictFiles = func(_ context.Context, binding string, input json.RawMessage, _ []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				calls++
				originalBinding, originalInput = binding, append(json.RawMessage(nil), input...)
				return nil, errors.New("original upload unknown")
			}
			spec.VerdictFilesIdentity = func(_ context.Context, binding string, input json.RawMessage) (string, error) {
				if binding != originalBinding || !bytes.Equal(input, originalInput) {
					return "", errors.New("original input changed")
				}
				return strings.Repeat("b", 64), nil
			}
			v := Verdicts{Spec: spec}
			input := `{"verdict":"blocked","evidence":{"check":"original"},"issues":["repair"],"files":[{"path":"checks.log","name":"checks.log"}]}`
			call := ToolCall{ThreadID: "thread_original", TurnID: "turn_original", Tool: VerdictTool, Arguments: json.RawMessage(input)}
			if got := v.Handle(t.Context(), call); got.Success {
				t.Fatal("unknown upload acknowledged")
			}
			before := snapshotVerdictState(t, dir)
			switch change {
			case "evidence":
				input = strings.Replace(input, `"original"`, `"updated"`, 1)
			case "issues":
				input = strings.Replace(input, `"repair"`, `"other"`, 1)
			case "path":
				input = strings.Replace(input, `"path":"checks.log"`, `"path":"other.log"`, 1)
			case "name":
				input = strings.Replace(input, `"name":"checks.log"`, `"name":"other.log"`, 1)
			case "binding":
				call.TurnID = "turn_other"
			}
			call.Arguments = json.RawMessage(input)
			reopened := Verdicts{Spec: spec}
			if got := reopened.Handle(t.Context(), call); got.Success {
				t.Fatal("changed pending acknowledged")
			}
			if calls != 1 {
				t.Fatalf("changed pending reached Seal: %d", calls)
			}
			if after := snapshotVerdictState(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatal("changed pending replaced original state")
			}
		})
	}
}

func TestUnreadableAcknowledgedVerdictRejectsWithoutEffects(t *testing.T) {
	for _, kind := range []string{"directory", "malformed", "missing identity", "tampered native binding", "tampered manifest"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "verdict.json")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				raw := `{"binding":"","native_binding":"other","sealed_identity":"` + strings.Repeat("b", 64) + `","payload":{"verdict":"blocked","evidence":{}}}`
				if kind == "malformed" {
					raw = "{"
				}
				if kind == "missing identity" {
					raw = `{"binding":"","payload":{"verdict":"blocked","evidence":{}}}`
				}
				if kind == "tampered manifest" {
					raw = `{"binding":"","native_binding":"{\"ThreadID\":\"thread_original\",\"TurnID\":\"turn_original\"}","sealed_identity":"` + strings.Repeat("b", 64) + `","payload":{"verdict":"blocked","evidence":{},"files":[{"file_id":"file_fake","name":"check.log","size":1,"sha256":"bad"}]}}`
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			v := Verdicts{Spec: WorkerSpec{StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity, SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				calls++
				return nil, nil
			}}}
			before := snapshotVerdictState(t, dir)
			if got := v.Handle(t.Context(), ToolCall{ThreadID: "thread_original", TurnID: "turn_original", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{}}`)}); got.Success {
				t.Fatal("unreadable original ACK replaced")
			}
			if calls != 0 {
				t.Fatal("unreadable original ACK reached Seal")
			}
			if after := snapshotVerdictState(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatal("unreadable ACK rejection changed state")
			}
		})
	}
}

type verdictStateSnapshot struct {
	Mode     fs.FileMode
	Modified time.Time
	SHA256   [32]byte
}

func snapshotVerdictState(t *testing.T, dir string) map[string]verdictStateSnapshot {
	t.Helper()
	snapshot := map[string]verdictStateSnapshot{}
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := verdictStateSnapshot{Mode: info.Mode(), Modified: info.ModTime()}
		if !entry.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.SHA256 = sha256.Sum256(raw)
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		snapshot[name] = state
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestFreshVerdictValidationPrecedesPendingIntent(t *testing.T) {
	for _, kind := range []string{"missing validator", "definite rejection", "unknown validation"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			validations, seals := 0, 0
			spec := WorkerSpec{StateDir: dir, AcceptanceEvidenceFiles: true, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					seals++
					return nil, errors.New("original seal unknown")
				}}
			if kind != "missing validator" {
				spec.ValidateVerdictFiles = func(context.Context, string, json.RawMessage, []VerdictFileSelection) error {
					validations++
					if _, err := os.Stat(filepath.Join(dir, "pending-verdict.json")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("fresh validation followed pending write: %v", err)
					}
					if kind == "definite rejection" {
						return errors.Join(ErrVerdictSelectionRejected, errors.New("source is definitely invalid"))
					}
					return errors.New("original identity read unknown")
				}
			}
			call := ToolCall{ThreadID: "thread_original", TurnID: "turn_original", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{"check":"original"},"files":[{"path":"checks.log","name":"checks.log"}]}`)}
			v := Verdicts{Spec: spec}
			if got := v.Handle(t.Context(), call); got.Success {
				t.Fatal("unvalidated verdict acknowledged")
			}
			if seals != 0 {
				t.Fatal("failed validation reached Seal")
			}
			_, err := os.Stat(filepath.Join(dir, "pending-verdict.json"))
			if kind == "unknown validation" {
				if err != nil {
					t.Fatalf("unknown validation discarded original pending: %v", err)
				}
				original := snapshotVerdictState(t, dir)
				changed := call
				changed.TurnID = "other_turn"
				next := Verdicts{Spec: spec}
				if got := next.Handle(t.Context(), changed); got.Success {
					t.Fatal("unknown validation scope replaced")
				}
				if after := snapshotVerdictState(t, dir); !reflect.DeepEqual(original, after) {
					t.Fatal("unknown validation state changed")
				}
				if err := next.RestoreContext(t.Context()); !errors.Is(err, ErrRetainedResumePending) {
					t.Fatalf("original unknown was not retained: %v", err)
				}
				if validations != 1 || seals != 1 {
					t.Fatalf("existing pending repeated fresh validation: validate=%d seal=%d", validations, seals)
				}
				return
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("definite/no validator produced pending: %v", err)
			}
			if kind == "missing validator" {
				if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
					t.Fatalf("missing validator wrote state: %v %+v", err, entries)
				}
				return
			}
			if entries, err := os.ReadDir(filepath.Join(dir, "verdict-rejections")); err != nil || len(entries) != 1 {
				t.Fatalf("definite rejection not preserved: %v %+v", err, entries)
			}
			// A crash/reopen after a definite read-only rejection has no pending
			// submission to adopt. A corrected native call can validate normally.
			spec.ValidateVerdictFiles = testValidateVerdictFiles
			spec.SealVerdictFiles = func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				seals++
				return nil, nil
			}
			corrected := Verdicts{Spec: spec}
			call.TurnID = "turn_corrected"
			call.Arguments = json.RawMessage(`{"verdict":"blocked","evidence":{"check":"corrected"}}`)
			if got := corrected.Handle(t.Context(), call); !got.Success {
				t.Fatalf("zero-effect rejection froze correction after reopen: %s", got.Text)
			}
		})
	}
}
