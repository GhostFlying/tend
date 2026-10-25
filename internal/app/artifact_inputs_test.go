package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestArtifactInputsAcrossIndependentWorkspacesAndBothRoles(t *testing.T) {
	h := newDeliverableHarness(t)
	source := h.createReportTask("source", temporaryReportSpec())
	producer, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no producer")
	}
	original := []byte("GIF89a\x00\xfforiginal frames\x00")
	file, _ := h.uploadResultFile(producer, "prototype.gif", original)
	unused, _ := h.uploadResultFile(producer, "unused.txt", []byte("not selected"))
	h.completeReport(producer, file, unused)
	artifactID := h.task(source.TaskID).CurrentArtifactID
	spec := temporaryReportSpec()
	// No relation or shared work item is necessary. The planner explicitly
	// selects an immutable original, not a directory or all source outputs.
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID, Name: "forged", Size: 99, SHA256: "forged", SourceMessageID: "forged", SourceTaskID: "forged", SourceArtifactID: "forged"}}
	env := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "consume", Spec: spec}, nil)
	created := h.exec(owner, env, CreateTask{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var target taskResult
	if err := json.Unmarshal(created.Result, &target); err != nil {
		t.Fatal(err)
	}
	counts := []int{h.count("tasks"), h.count("task_specs"), h.count("plan_actions"), h.count("deliverable_files")}
	if replay := h.exec(owner, env, CreateTask{}); !bytes.Equal(replay.Result, created.Result) {
		t.Fatalf("create replay changed result: %+v", replay)
	}
	for i, table := range []string{"tasks", "task_specs", "plan_actions", "deliverable_files"} {
		if h.count(table) != counts[i] {
			t.Fatalf("create replay changed %s", table)
		}
	}
	want := protocol.TaskInput{FileID: file.FileID, Name: file.Name, Size: file.Size, SHA256: file.SHA256, SourceTaskID: source.TaskID, SourceArtifactID: artifactID}
	frozen := h.storedSpec(target.TaskID, 1)
	if len(frozen.Inputs) != 1 || frozen.Inputs[0] != want {
		t.Fatalf("incorrect frozen source: %+v", frozen.Inputs)
	}
	// An ID-only update retains provenance and does not change intent.
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	before := h.task(target.TaskID)
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(target.TaskID), updateIntentPayload{Spec: &spec}, rev(before.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(target.TaskID).SpecVersion != 1 {
		t.Fatalf("ID-only update changed intent: %+v", op)
	}
	runtime := testRuntime("input-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl, ok := h.claimInputRun(runtime)
	if !ok || impl.TaskID != target.TaskID || impl.Workspace.ID == producer.Workspace.ID {
		t.Fatalf("not an independent execution: %+v", impl)
	}
	checkDownload := func(as protocol.Assignment) {
		t.Helper()
		meta, body, err := h.app.OpenRunFile(context.Background(), runPrincipal(as.RunID, as.Role), file.FileID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || !bytes.Equal(got, original) || meta != file {
			t.Fatalf("original changed: %+v %q %v", meta, got, err)
		}
		if _, body, err := h.app.OpenRunFile(context.Background(), runPrincipal(as.RunID, as.Role), unused.FileID); err == nil {
			body.Close()
			t.Fatal("unselected output became downloadable")
		}
	}
	checkDownload(impl)
	op = h.runAs(impl, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Inspected original bytes"}}, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.Role != protocol.RoleAccept || accept.TaskID != target.TaskID {
		t.Fatalf("no independent acceptance: %+v", accept)
	}
	checkDownload(accept)
	// Changing the input set only changes a future execution. The active
	// acceptance still reads its original spec, never the task's newest spec.
	spec.Inputs = []protocol.TaskInput{{FileID: unused.FileID}}
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(target.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(target.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(target.TaskID).SpecVersion != 2 {
		t.Fatalf("input revision: %+v", op)
	}
	checkDownload(accept)
	if h.storedSpec(target.TaskID, 1).Inputs[0] != want || h.storedSpec(target.TaskID, 2).Inputs[0].FileID != unused.FileID {
		t.Fatal("input history changed")
	}
}

func TestArtifactInputsRequireSubmittedMembershipNotAcceptance(t *testing.T) {
	h := newDeliverableHarness(t)
	source := h.createReportTask("source", temporaryReportSpec())
	producer, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal("no producer")
	}
	file, _ := h.uploadResultFile(producer, "findings.txt", []byte("unverified findings"))
	unused, _ := h.uploadResultFile(producer, "draft.txt", []byte("not submitted"))
	spec := temporaryReportSpec()
	assertRejected := func(id string) {
		t.Helper()
		before := make(map[string]int)
		for _, table := range []string{"tasks", "task_specs", "plan_actions", "work_items", "events", "outbox"} {
			before[table] = h.count(table)
		}
		spec.Inputs = []protocol.TaskInput{{FileID: id}}
		op, _ := h.createTask(owner, createTaskPayload{Title: "invalid", Spec: spec})
		if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeNotFound || !strings.Contains(op.Error.Message, "submitted artifact") {
			t.Fatalf("unsubmitted file not rejected clearly: %+v", op)
		}
		for table, count := range before {
			if h.count(table) != count {
				t.Fatalf("failed binding changed %s", table)
			}
		}
	}
	assertRejected(file.FileID)
	op := h.runAs(producer, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Findings to verify"}, Files: []protocol.ArtifactFile{file}}, SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assertRejected(unused.FileID)
	// Finalized but not yet accepted evidence is available for explicit use.
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	target := h.createReportTask("investigate evidence", spec)
	if got := h.storedSpec(target.TaskID, 1).Inputs[0]; got.SourceTaskID != source.TaskID || got.SourceArtifactID == "" {
		t.Fatalf("missing actual source: %+v", got)
	}
	accept, ok, _ := h.claimDeliverableRun()
	if !ok || accept.Role != protocol.RoleAccept || accept.TaskID != source.TaskID {
		t.Fatalf("no source acceptance: %+v", accept)
	}
	op = h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired}, SubmitAcceptance{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A reviewer rejecting the result does not make its original evidence
	// unavailable to a later investigation. No passed verdict is manufactured.
	h.createReportTask("investigate rejected evidence", spec)
	// A follow-up relationship by itself does not include any input files.
	op, follow := h.createTask(owner, createTaskPayload{Title: "relation only", Spec: temporaryReportSpec(), Relations: []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: source.TaskID}}})
	if op.Status != protocol.OpSucceeded || len(h.storedSpec(follow.TaskID, 1).Inputs) != 0 {
		t.Fatalf("relation copied outputs: %+v", op)
	}
	// The same resolver handles adding an artifact input through a revision.
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(follow.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(follow.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(follow.TaskID).SpecVersion != 2 || h.storedSpec(follow.TaskID, 2).Inputs[0].SourceTaskID != source.TaskID {
		t.Fatalf("adding artifact input failed: %+v", op)
	}
}

func TestArtifactInputProvenanceDoesNotChangeRunnerWire(t *testing.T) {
	spec := temporaryReportSpec()
	spec.Inputs = []protocol.TaskInput{{FileID: "original", Name: "file.txt", Size: 17, SHA256: "digest", SourceTaskID: "source-task", SourceArtifactID: "source-artifact"}}
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		raw, err := workerSpec(spec, role)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Inputs []json.RawMessage `json:"inputs"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil || len(wire.Inputs) != 1 {
			t.Fatalf("invalid assignment: %s %v", raw, err)
		}
		// Decode with the original v1 runner's field set and strict decoding.
		var legacy struct {
			FileID          string `json:"file_id"`
			Name            string `json:"name,omitempty"`
			MediaType       string `json:"media_type,omitempty"`
			SHA256          string `json:"sha256,omitempty"`
			SourceMessageID string `json:"source_message_id,omitempty"`
			Size            int64  `json:"size,omitempty"`
		}
		decoder := json.NewDecoder(bytes.NewReader(wire.Inputs[0]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&legacy); err != nil || legacy.FileID != "original" || legacy.SHA256 != "digest" || legacy.Size != 17 {
			t.Fatalf("legacy runner input changed: %+v %v", legacy, err)
		}
		if spec.Inputs[0].SourceTaskID != "source-task" || spec.Inputs[0].SourceArtifactID != "source-artifact" {
			t.Fatal("projection changed frozen provenance")
		}
	}
}
