package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSubmittedSharedReviewUsesPublishedSchemaAndSurvivesRestart(t *testing.T) {
	type objectSchema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	readObject := func(raw json.RawMessage) objectSchema {
		t.Helper()
		var schema objectSchema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			t.Fatal("result objects must retain their strict published schema")
		}
		return schema
	}
	root := readObject(ResultToolSpec.InputSchema)
	reportSchema := readObject(root.Properties["report"])
	reviewSchema := readObject(reportSchema.Properties["shared_review"])
	for _, key := range []string{"review_id", "head_sha", "base_sha", "requirements_digest", "verdict", "repository_check"} {
		if !slices.Contains(reviewSchema.Required, key) {
			t.Fatalf("review schema does not require frozen result field %q", key)
		}
	}
	var findingsSchema struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(reviewSchema.Properties["findings"], &findingsSchema); err != nil {
		t.Fatal(err)
	}
	findingSchema := readObject(findingsSchema.Items)
	for _, verdict := range []string{"passed", "changes_requested"} {
		t.Run(verdict, func(t *testing.T) {
			review := &protocol.SharedReviewResult{ReviewID: "review_frozen", HeadSHA: "head", BaseSHA: "base", RequirementsDigest: "requirements", Verdict: verdict, RepositoryCheck: "Reviewed the complete PR and repository instructions"}
			if verdict == "changes_requested" {
				review.Findings = []protocol.SharedReviewFinding{{ID: "F1", Priority: "P2", Body: "Preserve the pending result during recovery"}}
			}
			report := protocol.ResultReport{Body: "Complete PR review", Evidence: []string{"frozen candidate"}, Uncertainties: []string{"no deployment performed"}, SharedReview: review}
			arguments, err := json.Marshal(map[string]any{"report": report})
			if err != nil {
				t.Fatal(err)
			}
			// Check the actual serialized report against the advertised strict
			// object boundaries that previously excluded every shared review.
			for _, tc := range []struct {
				value  any
				schema objectSchema
			}{{report, reportSchema}, {review, reviewSchema}} {
				encoded, err := json.Marshal(tc.value)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				for key := range fields {
					if len(tc.schema.Properties[key]) == 0 {
						t.Fatalf("published submit_result schema rejects report field %q", key)
					}
				}
			}
			for _, key := range []string{"id", "priority", "body"} {
				if len(findingSchema.Properties[key]) == 0 {
					t.Fatalf("published finding schema rejects %q", key)
				}
			}
			spec := WorkerSpec{StateDir: t.TempDir(), Spec: json.RawMessage(`{"deliverables":["report"]}`)}
			r := Results{Spec: spec}
			if got := r.Handle(t.Context(), ToolCall{Tool: ResultTool, Arguments: arguments}); !got.Success {
				t.Fatalf("actual worker submission failed: %+v", got)
			}
			restored := Results{Spec: spec}
			if err := restored.Restore(); err != nil {
				t.Fatal(err)
			}
			if got := restored.Result(); got == nil || got.Report == nil || !reflect.DeepEqual(got.Report.SharedReview, review) {
				t.Fatalf("worker result lost immutable shared review: %+v", got)
			}
		})
	}
}

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
