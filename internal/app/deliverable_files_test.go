package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) uploadResultFile(as protocol.Assignment, name string, content []byte) (protocol.ArtifactFile, *protocol.Envelope) {
	h.t.Helper()
	digest := sha256.Sum256(content)
	manifest := protocol.ArtifactFile{Name: name, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	env := h.env(OpRunsUploadFile, runTarget(as.RunID), manifest, nil)
	op := h.exec(runPrincipal(as.RunID, as.Role), env, UploadFile{Content: content})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("upload: %+v", op)
	}
	var file protocol.ArtifactFile
	if err := json.Unmarshal(op.Result, &file); err != nil {
		h.t.Fatal(err)
	}
	return file, env
}

func TestReportResultWithoutGitChangesPassesIndependentAcceptance(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := testSpec
	spec.Deliverables = []string{domain.DeliveryReport}
	_, task := h.createTask(owner, createTaskPayload{Title: "investigate", Spec: spec})
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no implementation")
	}
	report := &protocol.ResultReport{Body: "# Findings\nNo code changes required.", Evidence: []string{"checked both paths"}}
	result := testArtifact(h.task(task.TaskID).WorkspaceName)
	result.Mounts[0].Changed = false
	result.Report = report
	op := h.runAs(as, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("submit report: %+v", op)
	}
	accept, ok, _ := h.claimDeliverableRun()
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatalf("accept assignment: %+v", accept)
	}
	var saved protocol.ArtifactPayload
	if err := json.Unmarshal(accept.Artifact, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Report == nil || saved.Report.Body != report.Body || saved.Mounts[0].Changed {
		t.Fatalf("saved result: %+v", saved)
	}
	op = h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("accept: %+v", op)
	}
	if got := h.task(task.TaskID).Lifecycle; got != protocol.TaskCompleted {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestRequiredDeliverablesAndOptionalReport(t *testing.T) {
	for _, kinds := range [][]string{{domain.DeliveryReport}, {domain.DeliveryFiles}, {domain.DeliveryCommit, domain.DeliveryReport}} {
		t.Run(strings.Join(kinds, "+"), func(t *testing.T) {
			h := newDeliverableHarness(t)
			spec := testSpec
			spec.Deliverables = kinds
			_, task := h.createTask(owner, createTaskPayload{Title: "outputs", Spec: spec})
			as, _, _ := h.claimDeliverableRun()
			op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{})
			if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
				t.Fatalf("missing output accepted: %+v", op)
			}
		})
	}
	h := newDeliverableHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "code and optional report", Spec: testSpec})
	as, _, _ := h.claimDeliverableRun()
	result := testArtifact(h.task(task.TaskID).WorkspaceName)
	result.Report = &protocol.ResultReport{Body: "Additional context"}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("optional report rejected: %+v", op)
	}
}

func TestFilesAreImmutableDeduplicatedAndAcceptanceScoped(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := testSpec
	spec.Deliverables = []string{domain.DeliveryReport, domain.DeliveryFiles}
	h.createTask(owner, createTaskPayload{Title: "files", Spec: spec})
	as, _, _ := h.claimDeliverableRun()
	bytes := []byte("durable evidence\n")
	file, env := h.uploadResultFile(as, "evidence.txt", bytes)
	readWindow := func(offset int64, count int) (store.DeliverableFileRow, error) {
		var window store.DeliverableFileRow
		err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
			var err error
			window, err = store.ReadFinalizedDeliverableFile(context.Background(), tx.Q(), file.FileID, offset, count)
			return err
		})
		return window, err
	}
	if _, err := readWindow(0, 4); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unfinalized file readable: %v", err)
	}

	replay := h.exec(runPrincipal(as.RunID, as.Role), env, UploadFile{Content: bytes})
	var repeated protocol.ArtifactFile
	_ = json.Unmarshal(replay.Result, &repeated)
	if repeated != file {
		t.Fatalf("upload replay changed identity: %+v != %+v", repeated, file)
	}
	result := protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Evidence attached"}, Files: []protocol.ArtifactFile{file}}
	forged := result
	forged.Files = append([]protocol.ArtifactFile(nil), result.Files...)
	forged.Files[0].SHA256 = strings.Repeat("0", 64)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, forged, SubmitArtifact{}); op.Status != protocol.OpFailed {
		t.Fatalf("forged manifest accepted: %+v", op)
	}
	// Both manifests can be authentic uploads yet still collide in a result.
	// The worker tool must reject these before acknowledging completion.
	sameName, _ := h.uploadResultFile(as, file.Name, []byte("second immutable evidence"))
	for _, files := range [][]protocol.ArtifactFile{{file, file}, {file, sameName}} {
		duplicate := result
		duplicate.Files = files
		op := h.runAs(as, protocol.OpRunsSubmitArtifact, duplicate, SubmitArtifact{})
		if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(op.Error.Message, "listed twice") {
			t.Fatalf("duplicate file identity/name accepted or rejected for wrong reason: %+v", op)
		}
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("submit files: %+v", op)
	}
	window, err := readWindow(2, 4)
	if err != nil || string(window.Content) != "rabl" || window.Size != int64(len(bytes)) {
		t.Fatalf("bounded file read: %+v %v", window, err)
	}
	if _, err := readWindow(0, 65537); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("oversize window: %v", err)
	}
	if window, err := readWindow(1<<62, 4); err != nil || len(window.Content) != 0 {
		t.Fatalf("beyond EOF: %+v %v", window, err)
	}
	accept, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no acceptance")
	}
	for _, p := range []protocol.Principal{owner, runPrincipal(as.RunID, as.Role), runPrincipal(accept.RunID, accept.Role)} {
		got, err := h.app.DeliverableFile(context.Background(), p, file.FileID)
		if err != nil || string(got.Content) != string(bytes) {
			t.Fatalf("read persisted file: %q %v", got.Content, err)
		}
	}
	_, err = h.app.DeliverableFile(context.Background(), mainAgent, file.FileID)
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("unexpected unscoped file access: %v", err)
	}
	_, otherTask := h.createTask(owner, createTaskPayload{Title: "other task", Spec: spec})
	other, _, _ := h.claimDeliverableRun()
	if other.TaskID != otherTask.TaskID {
		t.Fatalf("wrong run: %+v", other)
	}
	_, err = h.app.DeliverableFile(context.Background(), runPrincipal(other.RunID, other.Role), file.FileID)
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("cross-task read allowed: %v", err)
	}
	op := h.runAs(other, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{})
	if op.Status != protocol.OpFailed {
		t.Fatalf("cross-run file reference accepted: %+v", op)
	}
}

func TestUploadLimitsAndNameValidation(t *testing.T) {
	h := newDeliverableHarness(t)
	h.createTask(owner, createTaskPayload{Title: "files", Spec: testSpec})
	as, _, _ := h.claimDeliverableRun()
	for _, name := range []string{"../secret", "x\\y", "x\ny", "."} {
		body := []byte("abc")
		sum := sha256.Sum256(body)
		payload := protocol.ArtifactFile{Name: name, Size: 3, SHA256: hex.EncodeToString(sum[:])}
		op := h.exec(runPrincipal(as.RunID, as.Role), h.env(OpRunsUploadFile, runTarget(as.RunID), payload, nil), UploadFile{Content: body})
		if op.Status != protocol.OpFailed {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
	body := []byte("abc")
	sum := sha256.Sum256(body)
	payload := protocol.ArtifactFile{Name: "a.txt", Size: 3, SHA256: hex.EncodeToString(sum[:])}
	op := h.exec(runPrincipal(as.RunID, as.Role), h.env(OpRunsUploadFile, runTarget(as.RunID), payload, nil), UploadFile{Content: body, FileLimit: 2})
	if op.Status != protocol.OpFailed {
		t.Fatalf("oversize accepted: %+v", op)
	}
	h.uploadResultFile(as, "first.txt", body)
	op = h.exec(runPrincipal(as.RunID, as.Role), h.env(OpRunsUploadFile, runTarget(as.RunID), payload, nil), UploadFile{Content: body, ResultLimit: 5})
	if op.Status != protocol.OpFailed {
		t.Fatalf("aggregate limit exceeded: %+v", op)
	}
}

// newDeliverableHarness declares a runner supporting composable outputs.
func newDeliverableHarness(t *testing.T) *harness {
	h := newHarness(t)
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil), Hello{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("runner hello: %+v", op)
	}
	return h
}

func (h *harness) claimDeliverableRun() (protocol.Assignment, bool, *protocol.Envelope) {
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1}, nil)
	as, ok := h.claimWith("linux-1", env)
	return as, ok, env
}
