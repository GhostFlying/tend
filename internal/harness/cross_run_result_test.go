package harness

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestResultReusesConfirmedFileManifestWithoutUpload(t *testing.T) {
	original := protocol.ArtifactFile{FileID: "file_original", Name: "original.log", Size: 42, SHA256: "original-digest"}
	calls := 0
	spec := WorkerSpec{StateDir: t.TempDir(), Spec: json.RawMessage(`{"deliverables":["report","files"]}`), UploadFile: func(context.Context, string, string) (protocol.ArtifactFile, error) {
		calls++
		t.Fatal("original was uploaded again")
		return protocol.ArtifactFile{}, nil
	}}
	arguments, err := json.Marshal(map[string]any{"report": protocol.ResultReport{Body: "Original evidence, not a rerun", Evidence: []string{"original Run"}}, "existing_files": []protocol.ArtifactFile{original}})
	if err != nil {
		t.Fatal(err)
	}
	r := Results{Spec: spec}
	if got := r.Handle(t.Context(), ToolCall{Tool: ResultTool, Arguments: arguments}); !got.Success || calls != 0 {
		t.Fatal(got, calls)
	}
	restored := Results{Spec: spec}
	if err := restored.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := restored.Result(); got == nil || !reflect.DeepEqual(got.Files, []protocol.ArtifactFile{original}) || got.Report.Evidence[0] != "original Run" {
		t.Fatal("original metadata or provenance lost", got)
	}
}

func TestMixedResultRejectsDuplicateNameBeforeUpload(t *testing.T) {
	calls := 0
	r := Results{Spec: WorkerSpec{UploadFile: func(context.Context, string, string) (protocol.ArtifactFile, error) {
		calls++
		return protocol.ArtifactFile{}, nil
	}}}
	got := r.Handle(t.Context(), ToolCall{Tool: ResultTool, Arguments: json.RawMessage(`{"existing_files":[{"file_id":"file_original","name":"same.log","size":2,"sha256":"digest"}],"files":[{"path":"new.log","name":"same.log"}]}`)})
	if got.Success || calls != 0 {
		t.Fatal("duplicate caused an upload", got, calls)
	}
}
