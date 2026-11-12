package app

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func continueFileRun(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, as protocol.Assignment, dependency string) protocol.Assignment {
	t.Helper()
	wait := waitForTask(t, h, as, dependency, 0)
	// Only satisfy the synthetic dependency. The producing Run still stops via
	// the actual wait/physical-exit protocol before its successor can claim.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='completed' WHERE task_id=?`, dependency)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if as.Role == protocol.RoleIssueManager {
		stopOwnerClaim(t, h, as)
	} else if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID, ThreadID: "original-upload-session"}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	var next protocol.Assignment
	if as.Role == protocol.RoleIssueManager {
		next = ownerClaim(t, h, runtime)
	} else {
		var ok bool
		next, ok = h.claimInputRun(runtime)
		if !ok {
			t.Fatal("no successor")
		}
		if op := h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if next.TaskID != as.TaskID || next.RunID == as.RunID || h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("not a stopped same-Task continuation", next)
	}
	return next
}

func requireOriginalUpload(t *testing.T, h *harness, current, origin protocol.Assignment, file protocol.ArtifactFile, body string) {
	t.Helper()
	p := runPrincipal(current.RunID, current.Role)
	context, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files", ID: file.FileID})
	if err != nil || len(context.Materials.Files) != 1 || context.Materials.Files[0].ArtifactFile != file {
		t.Fatal("original not discoverable", context, err)
	}
	hasOrigin := false
	for _, source := range context.Materials.Files[0].Sources {
		if source.Kind == "upload" && source.TaskID == origin.TaskID && source.Locator == "run:"+origin.RunID && source.Status == "uploaded" {
			hasOrigin = true
		}
	}
	if !hasOrigin {
		t.Fatal("lost original upload Run", context.Materials.Files[0])
	}
	metadata, reader, err := h.app.OpenRunContextFile(t.Context(), p, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(got) != body || metadata.ArtifactFile != file {
		t.Fatal("original bytes changed", string(got), readErr, closeErr)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		saved, err := store.GetDeliverableFile(t.Context(), tx.Q(), file.FileID)
		if err == nil && (saved.RunID != origin.RunID || saved.TaskID != origin.TaskID || saved.ArtifactFile != file) {
			t.Fatal("upload provenance rewritten", saved)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskContinuationReusesOriginalUploadWithoutReupload(t *testing.T) {
	h, runtime, as, other := externalWaitFixture(t)
	body := "Original check log for the recorded candidate, not a rerun.\n"
	file, _ := h.uploadResultFile(as, "original-check.log", []byte(body))
	next := continueFileRun(t, h, runtime, as, other)
	requireOriginalUpload(t, h, next, as, file, body)
	// Same-Task raw recovery does not publish a finalized bounded file window.
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := store.ReadFinalizedDeliverableFile(t.Context(), tx.Q(), file.FileID, 0, 8, h.store.FileStore())
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatal("raw upload treated as result", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result := testArtifact(h.task(next.TaskID).WorkspaceName)
	result.Report = &protocol.ResultReport{Body: "Reuse the retained original log; no new check is claimed.", Evidence: []string{"original Run " + as.RunID}}
	result.Files = []protocol.ArtifactFile{file}
	for _, field := range []string{"sha256", "name", "size"} {
		forged := result
		forged.Files = append([]protocol.ArtifactFile(nil), result.Files...)
		switch field {
		case "sha256":
			forged.Files[0].SHA256 = strings.Repeat("0", 64)
		case "name":
			forged.Files[0].Name = "renamed.log"
		case "size":
			forged.Files[0].Size++
		}
		if op := h.runAs(next, protocol.OpRunsSubmitArtifact, forged, SubmitArtifact{}); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
			t.Fatal("manifest drift accepted", field, op)
		}
	}
	if op := h.runAs(next, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("deliverable_files") != 1 {
		t.Fatal("continuation required another upload")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		original, artifact, err := store.GetFinalizedDeliverableFile(t.Context(), tx.Q(), file.FileID)
		if err != nil {
			return err
		}
		var registrant string
		if err = tx.Q().QueryRowContext(t.Context(), `SELECT run_id FROM artifacts WHERE artifact_id=?`, artifact).Scan(&registrant); err != nil {
			return err
		}
		if original.RunID != as.RunID || registrant != next.RunID {
			t.Fatal("upload and result registrant conflated", original, registrant)
		}
		window, err := store.ReadFinalizedDeliverableFile(t.Context(), tx.Q(), file.FileID, 0, 8, h.store.FileStore())
		if err == nil && string(window.Content) != body[:8] {
			t.Fatal("finalized original window changed", window)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerContinuationRegistersOldRawUploadWithCurrentRun(t *testing.T) {
	h, runtime, issue, as, _ := reviewManagerAssignment(t)
	body := "Retained actual stage check before any later installation.\n"
	file, _ := h.uploadResultFile(as, "stage-check.log", []byte(body))
	dependency := createCanonicalTaskForTest(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	next := continueFileRun(t, h, runtime, as, dependency)
	requireOriginalUpload(t, h, next, as, file, body)
	result := protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Recover the original stage log without claiming a new check or upgrade."}, Files: []protocol.ArtifactFile{file}}
	p := protocol.UpdateIssueDeliveryPayload{TaskID: next.TaskID, RunID: next.RunID, Action: "submit_result", Result: &result, IssueControlPayload: protocol.IssueControlPayload{Reason: "Register the retained original under the active continuation"}}
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	requireOriginalUpload(t, h, next, as, file, body)
	if h.count("deliverable_files") != 1 || h.count("acceptances") != 0 {
		t.Fatal("reupload or synthetic independent verdict")
	}
}

func TestForeignTaskCannotRecoverOrReferenceRawUpload(t *testing.T) {
	h, runtime, as, otherTask := externalWaitFixture(t)
	file, _ := h.uploadResultFile(as, "private-raw.log", []byte("original"))
	other, ok := h.claimInputRun(runtime)
	if !ok || other.TaskID != otherTask {
		t.Fatal("missing other Task", other)
	}
	if op := h.runAs(other, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, err := h.app.ReadRunContextFile(t.Context(), runPrincipal(other.RunID, other.Role), file.FileID)
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("foreign raw read was not rejected", err)
	}
	result := testArtifact(h.task(other.TaskID).WorkspaceName)
	result.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(other, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("foreign original registered", op)
	}
}
