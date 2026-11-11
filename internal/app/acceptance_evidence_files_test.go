package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func acceptanceEvidenceFixture(t *testing.T) (*harness, protocol.Assignment, protocol.Assignment, *protocol.RunnerRuntime) {
	t.Helper()
	h := newDeliverableHarness(t)
	runtime := testRuntime("acceptance-files")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAcceptanceEvidenceFiles, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := h.createReportTask("independent evidence", temporaryReportSpec())
	impl, ok := h.claimInputRun(runtime)
	if !ok || impl.TaskID != task.TaskID {
		t.Fatalf("implementation: %+v", impl)
	}
	op := h.runAs(impl, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Candidate to judge"}}, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatalf("acceptance: %+v", accept)
	}
	return h, impl, accept, runtime
}

func assertAcceptanceFileBytes(t *testing.T, h *harness, p protocol.Principal, file protocol.ArtifactFile, want []byte) {
	t.Helper()
	meta, body, err := h.app.OpenRunFile(t.Context(), p, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(body)
	_ = body.Close()
	if readErr != nil || meta != file || !bytes.Equal(got, want) {
		t.Fatalf("immutable original: %+v %q %v", meta, got, readErr)
	}
}

func TestAcceptanceEvidenceFilesFinalizeAllVerdictsAndReplay(t *testing.T) {
	for _, verdict := range []protocol.Verdict{protocol.VerdictPassed, protocol.VerdictChangesRequired, protocol.VerdictBlocked} {
		t.Run(string(verdict), func(t *testing.T) {
			h, _, accept, _ := acceptanceEvidenceFixture(t)
			if !accept.AcceptanceEvidenceFiles {
				t.Fatal("capable acceptance claim omitted evidence upload contract")
			}
			original := []byte("original failed checks\x00\xff\n")
			file, upload := h.uploadResultFile(accept, "checks.log", original)
			before := h.count("acceptances")
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				_, _, err := store.GetFinalizedAcceptanceFile(t.Context(), tx.Q(), file.FileID)
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("unassociated upload finalized: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			p := protocol.VerdictPayload{Verdict: verdict, Evidence: json.RawMessage(`{"result":"unknown","path":"ephemeral"}`), Files: []protocol.ArtifactFile{file}}
			env := h.env(protocol.OpAcceptancesSubmit, runTarget(accept.RunID), p, nil)
			op := h.exec(runPrincipal(accept.RunID, accept.Role), env, SubmitAcceptance{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if replay := h.exec(runPrincipal(accept.RunID, accept.Role), env, SubmitAcceptance{}); replay.Error == nil || replay.Error.Code != protocol.CodeInvalidTransition || h.count("acceptances") != before+1 {
				t.Fatalf("verdict replay: %+v", replay)
			}
			if replay := h.exec(runPrincipal(accept.RunID, accept.Role), upload, UploadFile{Content: original}); replay.Status != protocol.OpSucceeded {
				t.Fatalf("ended run cannot replay its original upload: %+v", replay)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				got, row, err := store.GetFinalizedAcceptanceFile(t.Context(), tx.Q(), file.FileID)
				if err != nil || got.ArtifactFile != file || row.RunID != accept.RunID || row.TaskID != accept.TaskID || row.Verdict != verdict || row.EvidenceJSON != string(p.Evidence) {
					t.Fatalf("finalized acceptance provenance: %+v %+v %v", got, row, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_, body, err := h.app.OpenDeliverableFile(t.Context(), owner, file.FileID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("owner original: %q %v", got, err)
			}
			assertAcceptanceFileBytes(t, h, runPrincipal(accept.RunID, accept.Role), file, original)
			lateID := h.env(OpRunsUploadFile, runTarget(accept.RunID), nil, nil).RequestID
			late, err := h.app.UploadDeliverableFile(t.Context(), runPrincipal(accept.RunID, accept.Role), lateID, "late.log", strings.NewReader("new evidence after verdict"))
			if err != nil || late.Status != protocol.OpFailed || late.Error.Code != protocol.CodeInvalidTransition || h.count("deliverable_files") != 1 {
				t.Fatalf("new upload after the final verdict: %+v %v", late, err)
			}
		})
	}
}

func TestAcceptanceEvidenceFilesRejectUnnegotiatedAndSpoofedUploads(t *testing.T) {
	h := newDeliverableHarness(t)
	_, legacy := h.toAcceptanceClaim()
	runtime := testRuntime("later-capable-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAcceptanceEvidenceFiles)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	id := h.env(OpRunsUploadFile, runTarget(legacy.RunID), nil, nil).RequestID
	if _, err := h.app.UploadDeliverableFile(t.Context(), runPrincipal(legacy.RunID, legacy.Role), id, "late.log", strings.NewReader("old run")); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("later Hello retroactively authorized an old claim: %v", err)
	}
	if op := h.runAs(legacy, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("legacy no-file verdict changed: %+v", op)
	}
	h, impl, accept, _ := acceptanceEvidenceFixture(t)
	for _, principal := range []protocol.Principal{owner, runner("linux-1"), runPrincipal(impl.RunID, protocol.RoleAccept), runPrincipal(accept.RunID, protocol.RoleImplement)} {
		id := h.env(OpRunsUploadFile, runTarget(principal.Subject), nil, nil).RequestID
		if _, err := h.app.UploadDeliverableFile(t.Context(), principal, id, "spoof.log", strings.NewReader("not authorized")); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatalf("spoofed upload %+v: %v", principal, err)
		}
	}
	if h.count("deliverable_files") != 0 {
		t.Fatal("unauthorized upload published metadata")
	}
}

func TestAcceptanceEvidenceFilesRejectForgedManifestAtomically(t *testing.T) {
	h, impl, accept, runtime := acceptanceEvidenceFixture(t)
	file, _ := h.uploadResultFile(accept, "result.log", []byte("failed proof"))
	sameName, _ := h.uploadResultFile(accept, file.Name, []byte("different bytes"))
	otherRun := file
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		otherRun.FileID = h.ids.New(protocol.PrefixFile)
		return tx.InsertDeliverableFile(t.Context(), store.DeliverableFileRow{ArtifactFile: otherRun, TaskID: impl.TaskID, RunID: impl.RunID, CreatedAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	badSize, badHash, badName, missing := file, file, file, file
	badSize.Size++
	badHash.SHA256 = strings.Repeat("0", 64)
	badName.Name = "changed.log"
	missing.FileID = h.ids.New(protocol.PrefixFile)
	otherTask := h.createReportTask("unrelated producer", temporaryReportSpec())
	foreign, ok := h.claimInputRun(runtime)
	if !ok || foreign.TaskID != otherTask.TaskID || foreign.Role != protocol.RoleImplement {
		t.Fatalf("unrelated run: %+v", foreign)
	}
	foreignFile, _ := h.uploadResultFile(foreign, "unrelated.log", []byte("not acceptance evidence"))
	for name, files := range map[string][]protocol.ArtifactFile{"duplicate ID": {file, file}, "duplicate name": {file, sameName}, "size": {badSize}, "hash": {badHash}, "name": {badName}, "cross run": {otherRun}, "cross task": {foreignFile}, "partial manifest": {file, missing}} {
		t.Run(name, func(t *testing.T) {
			beforeEvents := h.count("events")
			op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Files: files}, SubmitAcceptance{})
			if op.Status != protocol.OpFailed || h.count("acceptances") != 0 || h.count("events") != beforeEvents {
				t.Fatalf("invalid manifest changed business facts: %+v", op)
			}
			if row := h.task(accept.TaskID); row.Lifecycle == protocol.TaskCompleted {
				t.Fatal("invalid manifest completed task")
			}
		})
	}
}

func TestAcceptanceEvidenceFilesFeedbackAndExplicitInputKeepExactSource(t *testing.T) {
	h, impl, accept, runtime := acceptanceEvidenceFixture(t)
	original := []byte("original negative verdict evidence")
	file, _ := h.uploadResultFile(accept, "negative.log", original)
	unused, _ := h.uploadResultFile(accept, "private.log", []byte("unsubmitted draft"))
	op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Files: []protocol.ArtifactFile{file}}, SubmitAcceptance{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		AcceptanceID string `json:"acceptance_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.Generation != 2 || next.Feedback == nil || next.Feedback.AcceptanceID != result.AcceptanceID || len(next.Feedback.Files) != 1 || next.Feedback.Files[0] != file {
		t.Fatalf("exact feedback missing: %+v", next)
	}
	assertAcceptanceFileBytes(t, h, runPrincipal(next.RunID, next.Role), file, original)
	for _, p := range []protocol.Principal{runPrincipal(impl.RunID, impl.Role), runPrincipal(next.RunID, protocol.RoleAccept)} {
		if _, body, err := h.app.OpenRunFile(t.Context(), p, file.FileID); err == nil {
			body.Close()
			t.Fatalf("non-feedback run/role read original: %+v", p)
		}
	}
	if _, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(next.RunID, next.Role), unused.FileID); err == nil {
		body.Close()
		t.Fatal("feedback exposed an unselected upload")
	}
	spec := temporaryReportSpec()
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID, SourceArtifactID: "forged", SourceAcceptanceID: "forged"}}
	target := h.createReportTask("explicit evidence investigation", spec)
	input := h.storedSpec(target.TaskID, 1).Inputs[0]
	if input.SourceTaskID != accept.TaskID || input.SourceAcceptanceID != result.AcceptanceID || input.SourceArtifactID != "" || input.Name != file.Name || input.SHA256 != file.SHA256 {
		t.Fatalf("acceptance provenance replaced with artifact: %+v", input)
	}
	var issue string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		issue, err = store.GetTaskIssueOwner(t.Context(), tx.Q(), accept.TaskID)
		if err != nil {
			return err
		}
		if got, err := issueScopedFile(t.Context(), tx.Q(), issue, 0, file.FileID); err != nil || got != file {
			t.Fatalf("own Issue acceptance evidence unavailable: %+v %v", got, err)
		}
		if _, err := issueScopedFile(t.Context(), tx.Q(), "unrelated-issue", 0, file.FileID); err == nil {
			t.Fatal("acceptance evidence crossed Issue scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceEvidenceFilesInterruptedStreamHasNoFinalizedReference(t *testing.T) {
	h, _, accept, _ := acceptanceEvidenceFixture(t)
	p := runPrincipal(accept.RunID, accept.Role)
	id := h.env(OpRunsUploadFile, runTarget(accept.RunID), nil, nil).RequestID
	if _, err := h.app.UploadDeliverableFile(t.Context(), p, id, "evidence.log", &failedFileBody{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial upload: %v", err)
	}
	if h.count("deliverable_files") != 0 || h.count("acceptances") != 0 {
		t.Fatal("partial stream created a business reference")
	}
	first, err := h.app.UploadDeliverableFile(t.Context(), p, id, "evidence.log", strings.NewReader("complete original"))
	if err != nil || first.Status != protocol.OpSucceeded {
		t.Fatalf("same upload reconciliation: %+v %v", first, err)
	}
	replay, err := h.app.UploadDeliverableFile(t.Context(), p, id, "evidence.log", strings.NewReader("complete original"))
	if err != nil || !bytes.Equal(replay.Result, first.Result) || h.count("deliverable_files") != 1 {
		t.Fatalf("upload replay changed identity: %+v %v", replay, err)
	}
	different, err := h.app.UploadDeliverableFile(t.Context(), p, id, "evidence.log", strings.NewReader("substituted original"))
	if err != nil || different.Status != protocol.OpSucceeded || h.count("deliverable_files") != 2 {
		t.Fatalf("new content lacks separate immutable object: %+v %v", different, err)
	}
	var other protocol.ArtifactFile
	if json.Unmarshal(different.Result, &other) != nil || bytes.Equal(different.Result, first.Result) {
		t.Fatal("different content replaced the original identity")
	}
	var file protocol.ArtifactFile
	if err := json.Unmarshal(first.Result, &file); err != nil {
		t.Fatal(err)
	}
	spec := temporaryReportSpec()
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	before := h.count("tasks")
	if op, _ := h.createTask(owner, createTaskPayload{Title: "premature input", Spec: spec}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeNotFound || h.count("tasks") != before {
		t.Fatalf("unfinalized acceptance upload became an input: %+v", op)
	}
}

func TestAcceptanceEvidenceFilesNewInputRequiresExactCurrentIssueAuthority(t *testing.T) {
	h, _, accept, _ := acceptanceEvidenceFixture(t)
	original := []byte("original proof")
	file, _ := h.uploadResultFile(accept, "evidence.log", original)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked, Files: []protocol.ArtifactFile{file}}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	other := createCanonicalOwnerTestIssue(t, h)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		stale := temporaryReportSpec()
		stale.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
		x := &Exec{coordinationExecution: &store.HostedExecution{IssueID: other.IssueID, AssignmentEpoch: other.AssignmentEpoch + 1}}
		if err := freezeTaskInputs(t.Context(), x, tx.Q(), &stale, nil); protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Fatal("new Task write used a former responsibility", err)
		}
		selected := temporaryReportSpec()
		selected.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
		x.coordinationExecution.AssignmentEpoch = other.AssignmentEpoch
		if err := freezeTaskInputs(t.Context(), x, tx.Q(), &selected, nil); err != nil {
			return err
		}
		_, source, err := store.GetFinalizedAcceptanceFile(t.Context(), tx.Q(), file.FileID)
		if err != nil {
			return err
		}
		if len(selected.Inputs) != 1 || selected.Inputs[0].SHA256 != file.SHA256 || selected.Inputs[0].Size != file.Size || selected.Inputs[0].SourceAcceptanceID != source.AcceptanceID || selected.Inputs[0].SourceTaskID != accept.TaskID {
			t.Fatal("shared evidence lost its immutable acceptance source", selected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	metadata, body, err := h.app.OpenIssueMaterialFile(t.Context(), other.IssueID, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(body)
	body.Close()
	if err != nil || metadata.SHA256 != file.SHA256 || !bytes.Equal(actual, original) {
		t.Fatal("index/source reuse changed the original", metadata, err)
	}
	materialProjectForTest(t, h, other.IssueID, "isolated-acceptance-consumer")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		selected := temporaryReportSpec()
		selected.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
		x := &Exec{coordinationExecution: &store.HostedExecution{IssueID: other.IssueID, AssignmentEpoch: other.AssignmentEpoch}}
		if err := freezeTaskInputs(t.Context(), x, tx.Q(), &selected, nil); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal("cross-project unshared source became a new input", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertAcceptanceFileBytes(t, h, runPrincipal(accept.RunID, accept.Role), file, original)
}

func TestAcceptanceEvidenceFilesFreezeWholeFeedbackAcrossLaterVerdict(t *testing.T) {
	h, _, accept, runtime := acceptanceEvidenceFixture(t)
	first, _ := h.uploadResultFile(accept, "first.log", []byte("first original"))
	firstEvidence, firstIssues := json.RawMessage(`{"stage":1}`), json.RawMessage(`["first defect"]`)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Evidence: firstEvidence, Issues: firstIssues, Files: []protocol.ArtifactFile{first}}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.Feedback == nil {
		t.Fatal("no repair assignment")
	}
	want := *next.Feedback
	if op := h.runAs(next, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "A repaired candidate"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	secondAccept, ok := h.claimInputRun(runtime)
	if !ok || secondAccept.Role != protocol.RoleAccept {
		t.Fatalf("second acceptance: %+v", secondAccept)
	}
	second, _ := h.uploadResultFile(secondAccept, "second.log", []byte("second original"))
	if op := h.runAs(secondAccept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Evidence: json.RawMessage(`{"stage":2}`), Issues: json.RawMessage(`["second defect"]`), Files: []protocol.ArtifactFile{second}}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		run, err := store.GetRun(t.Context(), tx.Q(), next.RunID)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), next.RunID)
		if err != nil {
			return err
		}
		rebuilt, err := buildAssignment(t.Context(), tx.Q(), run, lease, DefaultLeaseTTL, h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		gotJSON, _ := json.Marshal(rebuilt.Feedback)
		wantJSON, _ := json.Marshal(want)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("frozen assignment mixed a later verdict with original files: %s != %s", gotJSON, wantJSON)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertAcceptanceFileBytes(t, h, runPrincipal(next.RunID, next.Role), first, []byte("first original"))
	if _, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(next.RunID, next.Role), second.FileID); err == nil {
		body.Close()
		t.Fatal("a later acceptance expanded the old feedback authority")
	}
}

func TestAcceptanceEvidenceFilesRequireRecoveryCapabilityButKeepLegacyFeedbackWire(t *testing.T) {
	h, _, accept, _ := acceptanceEvidenceFixture(t)
	legacyRuntime := testRuntime("old-recovery")
	legacyRuntime.Capabilities = append(legacyRuntime.Capabilities, protocol.CapabilityTaskInputs, protocol.CapabilityWorkerObservation, protocol.CapabilityAcceptanceExecution)
	if op := h.versionedHello("linux-1", legacyRuntime); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(op.Error.Message, "acceptance-evidence-files") {
		t.Fatalf("old runner silently recovered a new file contract: %+v", op)
	}
	file, _ := h.uploadResultFile(accept, "negative.log", []byte("original evidence"))
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Files: []protocol.ArtifactFile{file}}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.versionedHello("linux-1", legacyRuntime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(legacyRuntime)
	if !ok || next.Feedback == nil {
		t.Fatal("legacy feedback missing")
	}
	raw, _ := json.Marshal(next.Feedback)
	var legacy struct {
		Verdict  protocol.Verdict `json:"verdict"`
		Evidence json.RawMessage  `json:"evidence,omitempty"`
		Issues   json.RawMessage  `json:"issues,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil || legacy.Verdict != protocol.VerdictChangesRequired {
		t.Fatalf("new fields leaked into legacy feedback: %s %v", raw, err)
	}
	if _, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(next.RunID, next.Role), file.FileID); err == nil {
		body.Close()
		t.Fatal("unnegotiated feedback gained file authority")
	}
	inputSpec := temporaryReportSpec()
	inputSpec.Inputs = []protocol.TaskInput{{FileID: file.FileID, Name: file.Name, Size: file.Size, SHA256: file.SHA256, SourceTaskID: accept.TaskID, SourceAcceptanceID: "immutable-acceptance"}}
	raw, err := workerSpec(inputSpec, protocol.RoleImplement)
	if err != nil || bytes.Contains(raw, []byte("source_acceptance_id")) {
		t.Fatalf("server-only input provenance changed legacy worker wire: %s %v", raw, err)
	}
}

func TestAcceptanceEvidenceFilesTransactionFailureRetainsOriginalRunAndRequest(t *testing.T) {
	h, _, accept, _ := acceptanceEvidenceFixture(t)
	file, _ := h.uploadResultFile(accept, "failure.log", []byte("failed original"))
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_acceptance_file_commit BEFORE UPDATE OF files_json ON acceptances WHEN NEW.files_json <> '[]' BEGIN SELECT RAISE(ABORT,'injected association failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpAcceptancesSubmit, runTarget(accept.RunID), protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Files: []protocol.ArtifactFile{file}}, nil)
	beforeEvents := h.count("events")
	if _, err := h.app.Execute(t.Context(), runPrincipal(accept.RunID, accept.Role), env, SubmitAcceptance{}); err == nil {
		t.Fatal("injected association transaction failure was acknowledged")
	}
	if h.count("acceptances") != 0 || h.count("events") != beforeEvents || h.task(accept.TaskID).Lifecycle == protocol.TaskCompleted {
		t.Fatal("failed association left a final verdict or task transition")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		run, err := store.GetRun(t.Context(), tx.Q(), accept.RunID)
		if err == nil && run.ObservedState == protocol.RunFinished {
			t.Fatal("failed association ended its run")
		}
		_, _, finalizedErr := store.GetFinalizedAcceptanceFile(t.Context(), tx.Q(), file.FileID)
		if !errors.Is(finalizedErr, store.ErrNotFound) {
			t.Fatalf("failed transaction finalized bytes: %v", finalizedErr)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_acceptance_file_commit`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runPrincipal(accept.RunID, accept.Role), env, SubmitAcceptance{}); op.Status != protocol.OpSucceeded || h.count("acceptances") != 1 {
		t.Fatalf("original request could not reconcile after rollback: %+v", op)
	}
}
