package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSubmittedResultSurvivesRestart(t *testing.T) {
	calls := 0
	spec := WorkerSpec{StateDir: t.TempDir(), UploadFile: func(_ context.Context, path, name string) (protocol.ArtifactFile, error) {
		calls++
		return protocol.ArtifactFile{FileID: "file_1", Name: name, Size: 4, SHA256: "digest"}, nil
	}}
	r := Results{Spec: spec}
	result := r.Handle(context.Background(), ToolCall{Tool: ResultTool, Arguments: json.RawMessage(`{"report":{"body":"investigation complete","evidence":["checked state"]},"files":[{"path":"evidence.txt","name":"evidence.txt"}]}`)})
	if !result.Success || calls != 1 {
		t.Fatalf("submission = %+v, uploads %d", result, calls)
	}
	restored := Results{Spec: spec}
	if err := restored.Restore(); err != nil {
		t.Fatal(err)
	}
	got := restored.Result()
	if got == nil || got.Report.Body != "investigation complete" || len(got.Files) != 1 || calls != 1 {
		t.Fatalf("restored = %+v", got)
	}
}

func TestAcceptanceDownloadUsesArtifactCallback(t *testing.T) {
	r := Results{Spec: WorkerSpec{DownloadFile: func(_ context.Context, id string) (string, error) {
		if id != "file_1" {
			t.Fatal(id)
		}
		return "scratch/file_1", nil
	}}}
	got := r.Handle(context.Background(), ToolCall{Tool: "download_file", Arguments: json.RawMessage(`{"file_id":"file_1"}`)})
	if !got.Success || got.Text != "scratch/file_1" {
		t.Fatalf("download = %+v", got)
	}
}

func TestInvalidResultRemainsCorrectableBeforeAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, files string
		returned    []protocol.ArtifactFile
		wantUploads int
	}{
		{"duplicate names", `[{"path":"first","name":"evidence"},{"path":"second","name":"evidence"}]`, nil, 0},
		{"repeated paths", `[{"path":"first","name":"a"},{"path":"./first","name":"b"}]`, nil, 0},
		{"duplicate IDs", `[{"path":"first","name":"a"},{"path":"second","name":"b"}]`, []protocol.ArtifactFile{{FileID: "same", Name: "a"}, {FileID: "same", Name: "b"}}, 2},
		{"duplicate manifest names", `[{"path":"first","name":"a"},{"path":"second","name":"b"}]`, []protocol.ArtifactFile{{FileID: "one", Name: "same"}, {FileID: "two", Name: "same"}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := Results{Spec: WorkerSpec{StateDir: t.TempDir(), Spec: json.RawMessage(`{"deliverables":["report","files"]}`), UploadFile: func(_ context.Context, path, name string) (protocol.ArtifactFile, error) {
				calls++
				if calls <= len(tc.returned) {
					return tc.returned[calls-1], nil
				}
				return protocol.ArtifactFile{FileID: "corrected", Name: name}, nil
			}}}
			bad := r.Handle(t.Context(), ToolCall{Tool: ResultTool, Arguments: json.RawMessage(`{"report":{"body":"report"},"files":` + tc.files + `}`)})
			if bad.Success || calls != tc.wantUploads || r.Result() != nil {
				t.Fatalf("invalid result acknowledged: %+v uploads=%d", bad, calls)
			}
			if _, err := os.Stat(filepath.Join(r.Spec.StateDir, "result.json")); !os.IsNotExist(err) {
				t.Fatalf("invalid result persisted: %v", err)
			}
			good := r.Handle(t.Context(), ToolCall{Tool: ResultTool, Arguments: json.RawMessage(`{"report":{"body":"corrected"},"files":[{"path":"corrected","name":"corrected"}]}`)})
			if !good.Success || r.Result() == nil {
				t.Fatalf("worker could not correct result: %+v", good)
			}
		})
	}
}

func TestRestoreRejectsResultsTheServerWouldReject(t *testing.T) {
	for _, body := range []string{
		`{"files":[{"file_id":"one","name":"same"},{"file_id":"two","name":"same"}]}`,
		`{"files":[{"file_id":"same","name":"one"},{"file_id":"same","name":"two"}]}`,
		`{"files":[{"name":"no-id"}]}`,
		`{"report":{"body":" "}}`,
	} {
		r := Results{Spec: WorkerSpec{StateDir: t.TempDir()}}
		if err := os.WriteFile(filepath.Join(r.Spec.StateDir, "result.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.Restore(); err == nil || r.Result() != nil {
			t.Fatalf("invalid restored result accepted: %s", body)
		}
	}
	r := Results{Spec: WorkerSpec{StateDir: t.TempDir(), Spec: json.RawMessage(`{"deliverables":["report","files"]}`)}}
	if err := os.WriteFile(filepath.Join(r.Spec.StateDir, "result.json"), []byte(`{"report":{"body":"missing required files"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(); err == nil || r.Result() != nil {
		t.Fatal("restore skipped required deliverables")
	}
}
