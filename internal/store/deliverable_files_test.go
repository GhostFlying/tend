package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFinalizedFileReferencesRetainHistoricalProducerScope(t *testing.T) {
	s, _ := openTest(t)
	acceptanceFileFixture(t, s)
	mustExec(t, s, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('successor-action','evidence-task','implement','successor-effect','{}',1,'{}',1,'done','successor-request',11,12)`)
	mustExec(t, s, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES('successor-run','evidence-task','successor-action',1,'implement','run','finished',11,12)`)
	original := []byte("unchanged original bytes\x00\n")
	sum := sha256.Sum256(original)
	file := protocol.ArtifactFile{FileID: "original-file", Name: "original.log", Size: int64(len(original)), SHA256: hex.EncodeToString(sum[:])}
	draft := file
	draft.FileID = "acceptance-draft"
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if err := tx.InsertDeliverableFile(t.Context(), DeliverableFileRow{ArtifactFile: file, TaskID: "evidence-task", RunID: "implement-run", Content: original, CreatedAt: 11}); err != nil {
			return err
		}
		return tx.InsertDeliverableFile(t.Context(), DeliverableFileRow{ArtifactFile: draft, TaskID: "evidence-task", RunID: "accept-run", Content: original, CreatedAt: 12})
	}); err != nil {
		t.Fatal(err)
	}
	full := func(f protocol.ArtifactFile) map[string]any {
		return map[string]any{"file_id": f.FileID, "name": f.Name, "size": f.Size, "sha256": f.SHA256}
	}
	wrong := file
	wrong.Name = "other.log"
	for _, tc := range []struct {
		name     string
		consumer string
		file     protocol.ArtifactFile
		entry    map[string]any
		allowed  bool
	}{
		{"original_ID_only", "implement-run", file, map[string]any{"file_id": file.FileID}, true},
		{"original_full", "implement-run", file, full(file), true},
		{"original_metadata_drift", "implement-run", file, full(wrong), false},
		{"original_partial", "implement-run", file, map[string]any{"file_id": file.FileID, "name": file.Name}, false},
		{"original_explicit_null", "implement-run", file, map[string]any{"file_id": file.FileID, "name": nil, "size": nil, "sha256": nil}, false},
		{"original_extra_field", "implement-run", file, map[string]any{"file_id": file.FileID, "other": "not an ID-only reference"}, false},
		{"successor_full", "successor-run", file, full(file), true},
		{"successor_ID_only", "successor-run", file, map[string]any{"file_id": file.FileID}, false},
		{"acceptance_draft_ID_only", "implement-run", draft, map[string]any{"file_id": draft.FileID}, false},
		{"acceptance_draft_full", "implement-run", draft, full(draft), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"files": []any{tc.entry}})
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s, `UPDATE artifacts SET run_id=?,ref_json=? WHERE artifact_id='evidence-artifact'`, tc.consumer, string(raw))
			metadata, artifact, err := GetFinalizedDeliverableFile(t.Context(), s.read, tc.file.FileID)
			window, windowErr := ReadFinalizedDeliverableFile(t.Context(), s.read, tc.file.FileID, 1, 5, s.FileStore())
			if !tc.allowed {
				if !errors.Is(err, ErrNotFound) || !errors.Is(windowErr, ErrNotFound) {
					t.Fatal("reference widened original publication", err, windowErr)
				}
				return
			}
			if err != nil || metadata.ArtifactFile != file || metadata.RunID != "implement-run" || artifact != "evidence-artifact" {
				t.Fatal("original provenance changed", metadata, artifact, err)
			}
			if windowErr != nil || window.ArtifactFile != file || !bytes.Equal(window.Content, original[1:6]) {
				t.Fatal("original window differs", window, windowErr)
			}
		})
	}
}
