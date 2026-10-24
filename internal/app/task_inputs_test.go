package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) claimInputRun(runtime *protocol.RunnerRuntime) (protocol.Assignment, bool) {
	h.t.Helper()
	return h.claimWith("linux-1", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil))
}

func TestTaskInputsFreezeOriginalMetadataAndFollowUpExplicitly(t *testing.T) {
	h := newDeliverableHarness(t)
	file := uploadChatFile(t, h, "animation.gif", "image/gif", []byte("GIF89a\x00original second frame"))
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "investigate", Attachments: []string{file.FileID}}, nil), SubmitMessage{})
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &message); err != nil || message.MessageID == "" {
		t.Fatalf("message %+v %v", op, err)
	}
	spec := testSpec
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID, Name: "invented", Size: 999, SHA256: "invented", SourceMessageID: "invented"}}
	op, task := h.createTask(owner, createTaskPayload{Title: "original", Spec: spec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	saved := h.storedSpec(task.TaskID, 1)
	want := protocol.TaskInput{FileID: file.FileID, Name: file.Name, MediaType: file.MediaType, Size: file.Size, SHA256: file.SHA256, SourceMessageID: message.MessageID}
	if len(saved.Inputs) != 1 || saved.Inputs[0] != want {
		t.Fatalf("frozen input %+v", saved.Inputs)
	}
	// Repeating only the ID is the same intent; caller metadata does not alter identity.
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(1)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 1 {
		t.Fatalf("identical input changed spec: %+v", op)
	}
	refs := []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: task.TaskID}}
	_, without := h.createTask(owner, createTaskPayload{Title: "no inheritance", Spec: testSpec, Relations: refs})
	if len(h.storedSpec(without.TaskID, 1).Inputs) != 0 {
		t.Fatal("follow-up implicitly copied input")
	}
	_, with := h.createTask(owner, createTaskPayload{Title: "explicit inheritance", Spec: spec, Relations: refs})
	if got := h.storedSpec(with.TaskID, 1).Inputs; len(got) != 1 || got[0] != want {
		t.Fatalf("explicit reuse lost provenance: %+v", got)
	}
	spec.Inputs = nil
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(1)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 2 {
		t.Fatalf("removing input did not change spec: %+v", op)
	}
}

func TestTaskInputsRejectMissingAndDuplicateReferencesAtomically(t *testing.T) {
	h := newDeliverableHarness(t)
	file := uploadChatFile(t, h, "input.bin", "application/octet-stream", []byte{0, 1, 255})
	missing := h.ids.New(protocol.PrefixFile)
	for _, inputs := range [][]protocol.TaskInput{{{FileID: missing}}, {{FileID: file.FileID}, {FileID: file.FileID}}, {{FileID: "bad"}}} {
		spec := testSpec
		spec.Inputs = inputs
		op, _ := h.createTask(owner, createTaskPayload{Title: "invalid", Spec: spec})
		if op.Status != protocol.OpFailed || h.count("tasks") != 0 || h.count("task_specs") != 0 {
			t.Fatalf("invalid inputs persisted: %+v", op)
		}
	}
}

func TestTaskInputDownloadsAreOriginalAndScopedToRunSpec(t *testing.T) {
	h := newDeliverableHarness(t)
	raw := []byte("GIF89a\x00\xfftwo original frames\x00")
	file := uploadChatFile(t, h, "animation.gif", "image/gif", raw)
	other := uploadChatFile(t, h, "other.bin", "application/octet-stream", []byte("unrelated"))
	spec := testSpec
	spec.Deliverables = []string{domain.DeliveryReport}
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	op, task := h.createTask(owner, createTaskPayload{Title: "inspect original", Spec: spec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A legacy runner can still register but cannot silently ignore inputs.
	if _, ok, _ := h.claimDeliverableRun(); ok {
		t.Fatal("legacy runner claimed input task")
	}
	runtime := testRuntime("input-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("capable runner did not claim")
	}
	assertDownload := func(as protocol.Assignment, id string, want []byte) {
		t.Helper()
		meta, body, err := h.app.OpenRunFile(context.Background(), runPrincipal(as.RunID, as.Role), id)
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		got, err := io.ReadAll(body)
		if err != nil || !bytes.Equal(got, want) || meta.SHA256 != file.SHA256 {
			t.Fatalf("original bytes changed: %+v %v", meta, err)
		}
	}
	assertDownload(impl, file.FileID, raw)
	if _, body, err := h.app.OpenRunFile(context.Background(), runPrincipal(impl.RunID, impl.Role), other.FileID); err == nil {
		body.Close()
		t.Fatal("unrelated attachment downloadable")
	}
	result := testArtifact(h.task(task.TaskID).WorkspaceName)
	result.Mounts[0].Changed = false
	result.Report = &protocol.ResultReport{Body: "Observed both frames"}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatalf("no acceptance: %+v", accept)
	}
	assertDownload(accept, file.FileID, raw)
	// A subsequent spec does not change the inputs of an already assigned run.
	spec.Inputs = []protocol.TaskInput{{FileID: other.FileID}}
	before := h.task(task.TaskID)
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(before.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assertDownload(accept, file.FileID, raw)
	if _, body, err := h.app.OpenRunFile(context.Background(), runPrincipal(accept.RunID, accept.Role), other.FileID); err == nil {
		body.Close()
		t.Fatal("old run gained a later spec's input")
	}
}

func TestTaskInputCapabilityQueueAndClaims(t *testing.T) {
	h := newDeliverableHarness(t)
	file := uploadChatFile(t, h, "notes.txt", "text/plain", []byte("notes"))
	spec := testSpec
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	_, task := h.createTask(owner, createTaskPayload{Title: "input", Spec: spec})
	checkQueue := func(wantUnsupported bool) {
		t.Helper()
		err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
			entries, err := Queue(context.Background(), tx.Q(), h.app.QueueOrder())
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.TaskID == task.TaskID {
					if strings.Contains(entry.Unplaceable, "input attachments") != wantUnsupported {
						t.Fatalf("queue reason %q", entry.Unplaceable)
					}
					return nil
				}
			}
			t.Fatal("input task missing from queue")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkQueue(true)
	runtime := testRuntime("capable")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	checkQueue(false)
	// Gate on this claim, not a stale stored observation from an older executable.
	runtime.Capabilities = nil
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("claim without capability used stored capability")
	}
	_, plain := h.createTask(owner, createTaskPayload{Title: "plain", Spec: testSpec})
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != plain.TaskID {
		t.Fatal("capability gating broke old no-input tasks")
	}
}

func TestTaskInputRecoveryRequiresOriginalCapability(t *testing.T) {
	h := newDeliverableHarness(t)
	file := uploadChatFile(t, h, "input.txt", "text/plain", []byte("original"))
	spec := testSpec
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	h.createTask(owner, createTaskPayload{Title: "inputs", Spec: spec})
	runtime := testRuntime("capable")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("no input run")
	}
	for _, held := range [][]protocol.HeldRun{nil, {{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}} {
		old := testRuntime("older-binary")
		old.Generation = runtime.Generation + 1
		env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: old, Held: held}, nil)
		_, err := h.app.Execute(context.Background(), runner("linux-1"), env, Hello{})
		if protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatalf("unsupported recovery: %v", err)
		}
	}
	// A capable restart can recover the same unacknowledged assignment.
	resumed := testRuntime("capable-restart")
	resumed.Capabilities = append(resumed.Capabilities, protocol.CapabilityTaskInputs)
	op := h.versionedHello("linux-1", resumed)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result protocol.HelloResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Reassigned) != 1 || result.Reassigned[0].RunID != as.RunID {
		t.Fatalf("input reassignment lost: %+v", result)
	}
}

func TestTaskInputsRetainFrozenProvenanceAfterLaterChatAttachment(t *testing.T) {
	h := newDeliverableHarness(t)
	file := uploadChatFile(t, h, "input.txt", "text/plain", []byte("original"))
	spec := testSpec
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	op, task := h.createTask(owner, createTaskPayload{Title: "original", Spec: spec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	frozen := h.storedSpec(task.TaskID, 1).Inputs[0]
	if frozen.SourceMessageID != "" {
		t.Fatalf("unexpected initial source: %+v", frozen)
	}
	before := relationDetail(t, h, task.TaskID)
	attach := func(fileID string) string {
		t.Helper()
		op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "the original input", Attachments: []string{fileID}}, nil), SubmitMessage{})
		var message struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(op.Result, &message); err != nil || message.MessageID == "" {
			t.Fatalf("attachment message: %+v %v", op, err)
		}
		return message.MessageID
	}
	attach(file.FileID)
	// Neither caller metadata nor a later chat binding changes this frozen input.
	spec.Inputs[0] = protocol.TaskInput{FileID: file.FileID, Name: "invented", Size: 999, SHA256: "invented", SourceMessageID: "invented"}
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(before.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := relationDetail(t, h, task.TaskID)
	if after.SpecVersion != before.SpecVersion || after.ControlRevision != before.ControlRevision || !slices.Equal(after.Actions, before.Actions) {
		t.Fatalf("unchanged intent changed task or actions: before=%+v after=%+v", before, after)
	}
	if got := h.storedSpec(task.TaskID, 1).Inputs[0]; got != frozen {
		t.Fatalf("frozen metadata changed: %+v", got)
	}

	// A genuine goal change still creates a version, retaining the original input.
	spec.Goal += " and document the result"
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(after.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 2 {
		t.Fatalf("goal update: %+v", op)
	}
	if got := h.storedSpec(task.TaskID, 2).Inputs[0]; got != frozen {
		t.Fatalf("new spec changed retained provenance: %+v", got)
	}

	// Newly selected inputs freeze the source available when they enter the spec.
	added := uploadChatFile(t, h, "additional.txt", "text/plain", []byte("new evidence"))
	source := attach(added.FileID)
	spec.Inputs = append(spec.Inputs, protocol.TaskInput{FileID: added.FileID, SourceMessageID: "invented"})
	op = h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 3 {
		t.Fatalf("new input update: %+v", op)
	}
	inputs := h.storedSpec(task.TaskID, 3).Inputs
	want := protocol.TaskInput{FileID: added.FileID, Name: added.Name, MediaType: added.MediaType, Size: added.Size, SHA256: added.SHA256, SourceMessageID: source}
	if len(inputs) != 2 || inputs[0] != frozen || inputs[1] != want {
		t.Fatalf("mixed retained and new inputs: %+v", inputs)
	}
}
