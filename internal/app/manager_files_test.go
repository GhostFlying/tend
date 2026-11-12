package app

import (
	"io"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerOriginalUploadDownloadsAcrossRuns(t *testing.T) {
	h, runtime, issue, original, _ := reviewManagerAssignment(t)
	const body = "Original unpublished manager evidence. 原始检查\x00\n"
	file, _ := h.uploadResultFile(original, "unpublished-manager.log", []byte(body))
	check := func(t *testing.T, as protocol.Assignment) {
		t.Helper()
		p := runPrincipal(as.RunID, as.Role)
		metadata, err := h.app.ReadRunContextFile(t.Context(), p, file.FileID)
		if err != nil || metadata.ArtifactFile != file {
			t.Fatalf("context path cannot resolve original upload: metadata=%+v err=%v", metadata, err)
		}
		direct, err := h.app.DeliverableFile(t.Context(), p, file.FileID)
		if err != nil || direct.ArtifactFile != file || direct.RunID != original.RunID {
			t.Fatalf("deliverable path changed original producer: metadata=%+v err=%v", direct, err)
		}
		if metadata, err := h.app.ManagerFile(t.Context(), p, file.FileID); err != nil || metadata != file {
			t.Fatalf("manager download lost original upload: metadata=%+v err=%v", metadata, err)
		}
		metadataFile, reader, err := h.app.OpenRunFile(t.Context(), p, file.FileID)
		if err != nil {
			t.Fatal("manager byte-download lost original upload:", err)
		}
		defer reader.Close()
		got, err := io.ReadAll(reader)
		if err != nil || string(got) != body || metadataFile != file {
			t.Fatalf("manager byte-download differs from original: body=%q metadata=%+v err=%v", got, metadataFile, err)
		}
	}
	t.Run("producing_run", func(t *testing.T) { check(t, original) })
	dependency := createCanonicalTaskForTest(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	next := continueFileRun(t, h, runtime, original, dependency)
	t.Run("same_task_successor", func(t *testing.T) { check(t, next) })
}
