package app

import (
	"encoding/json"
	"strings"
	"testing"

	workerharness "github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDirectOwnerRunMessageUsesActualIdentityAndFrozenTarget(t *testing.T) {
	h, as := messageRun(t)
	before := h.task(as.TaskID)
	payload := protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: "Inspect the attached evidence; this is not a scope update"}
	env := h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), payload, rev(before.ControlRevision))
	first := messageResult(t, h.exec(owner, env, SendRunMessage{}))
	replay := messageResult(t, h.exec(owner, env, SendRunMessage{}))
	if first.MessageID != replay.MessageID || first.Source != owner || first.SourceMessageID != "" || h.count("plan_commits") != 0 {
		t.Fatal("owner was rewritten as planner", first, replay)
	}
	if got := h.task(as.TaskID); got.ControlRevision != before.ControlRevision || got.SpecVersion != before.SpecVersion {
		t.Fatal("supplement changed the agreement")
	}
	claimed := claimMessage(t, h, as, first)
	accepted := messageResult(t, h.runAs(as, protocol.OpRunsReportMessage, protocol.RunMessageReceipt{RunMessageDelivery: claimed.RunMessageDelivery, Status: protocol.RunMessageAccepted}, ReportRunMessage{}))
	if accepted.Status != protocol.RunMessageAccepted || strings.Contains(workerharness.SupplementalContext(accepted), "authenticated main agent") {
		t.Fatal("receipt/provenance changed", accepted)
	}
	page, err := h.app.RunMessages(t.Context(), owner, as.RunID, 0, 100, false)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	payload.Generation++
	rejected := h.exec(owner, h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), payload, rev(before.ControlRevision)), SendRunMessage{})
	if rejected.Error == nil || rejected.Error.Code != protocol.CodeStaleRevision || h.count("run_messages") != 1 {
		t.Fatal("retargeted stale run", rejected)
	}
}

func TestDirectSourceInjectionPreservesExternalOriginalAndLinkedCorrection(t *testing.T) {
	h := newHarness(t)
	bytes := "original attachment bytes"
	uploaded, err := h.app.UploadConversationAttachment(t.Context(), owner, h.ids.New(protocol.PrefixRequest), "evidence.txt", "text/plain", strings.NewReader(bytes))
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	payload := protocol.InjectSourcePayload{ConversationID: "main", Text: "External text is evidence, not an owner's decision", Attachments: []string{file.FileID}, Metadata: protocol.ManualSourceMetadata{URL: "https://example.com/original"}}
	env := h.env("conversation.inject_source", protocol.Target{Type: "message"}, payload, nil)
	first := h.exec(owner, env, InjectSource{})
	if first.Status != protocol.OpAccepted {
		t.Fatal(first)
	}
	var original protocol.ManualSource
	if err := json.Unmarshal(first.Result, &original); err != nil {
		t.Fatal(err)
	}
	if original.Metadata.Author != "" || original.Metadata.AtMillis != nil || original.SuppliedBy != owner {
		t.Fatal("invented author or time", original)
	}
	if replay := h.exec(owner, env, InjectSource{}); string(first.Result) != string(replay.Result) {
		t.Fatal("source replay changed")
	}
	payload.Text = "Explicit linked correction"
	payload.Metadata.CorrectionOf = original.MessageID
	payload.Attachments = nil
	correction := h.exec(owner, h.env("conversation.inject_source", protocol.Target{Type: "message"}, payload, nil), InjectSource{})
	if correction.Status != protocol.OpAccepted {
		t.Fatal(correction)
	}
	reread, err := h.app.ManualSource(t.Context(), original.MessageID)
	if err != nil || reread.Text != original.Text || len(reread.Attachments) != 1 {
		t.Fatal("correction replaced original", reread, err)
	}
	_, reader, err := h.app.OpenConversationAttachment(t.Context(), file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw := make([]byte, len(bytes))
	if _, err := reader.Read(raw); err != nil || string(raw) != bytes {
		t.Fatal("original changed", err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := store.GetMessage(t.Context(), tx.Q(), original.MessageID)
		if m.Author != "external" {
			t.Fatal("external source became owner authority", m)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectManagerRunMessageUsesCurrentExecutionAndPrimaryScope(t *testing.T) {
	h := hostedManagerHarness(t)
	runtime := testRuntime("direct-manager-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityRunMessages)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, manager := startHostedIssueReview(t, h, issue)
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Check original evidence", Spec: testSpec, Rating: protocol.RatingP1}
	created := hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), payload, CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := hostedEffect(t, h, manager, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("manager task was not claimable")
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	message := protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: "Use this newly supplied evidence, retain the agreement"}
	op := hostedEffect(t, h, manager, turn, 3, runTarget(as.RunID), rev(h.task(as.TaskID).ControlRevision), message, SendRunMessage{})
	got := messageResult(t, op)
	if got.Source != manager || got.SourceMessageID != "" {
		t.Fatal("manager principal was replaced", got)
	}
	repeated := hostedEffect(t, h, manager, turn, 3, runTarget(as.RunID), rev(h.task(as.TaskID).ControlRevision), message, SendRunMessage{})
	if repeated.Error == nil || repeated.Error.Code != protocol.CodeDuplicateEffect || h.count("run_messages") != 1 {
		t.Fatal("native retry duplicated supplement", repeated)
	}
	if page, err := h.app.RunMessages(t.Context(), manager, as.RunID, 0, 100, false); err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	sibling := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	otherTurn, other := startHostedIssueReview(t, h, sibling)
	_, denied := h.app.Execute(t.Context(), other, h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), message, rev(h.task(as.TaskID).ControlRevision)), HostedCoordinationCommand{Command: SendRunMessage{}, ExecutionID: otherTurn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal("cross Issue write permitted", denied)
	}
	if _, err := h.app.RunMessages(t.Context(), other, as.RunID, 0, 100, false); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cross Issue read permitted", err)
	}
	handoff := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner assumes responsibility"}
	next := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, handoff, rev(issue.Revision)), HostedIssueAssignment{})
	if next.Status != protocol.OpSucceeded {
		t.Fatal(next)
	}
	_, denied = h.app.Execute(t.Context(), manager, h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), message, rev(h.task(as.TaskID).ControlRevision)), HostedCoordinationCommand{Command: SendRunMessage{}, ExecutionID: turn.RequestID, EffectSeq: 4})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal("fenced manager write permitted", denied)
	}
}

func TestDirectNamedRunSupplementKeepsFrozenWorkerWhenHostDefaultChanges(t *testing.T) {
	h, r, projection, _ := inventorySetup(t)
	projection.Agents[0].Categories = []string{"implementer"}
	projection.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, projection, hash, 1, 0, "supplement-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityRunMessages, protocol.CapabilityWorkerQuestions)
	r.JournalVersion = 12
	for _, a := range projection.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "supplement-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	h.createTask(owner, createTaskPayload{Title: "Frozen named supplement", Spec: spec})
	as, ok := h.claimInputRun(r)
	if !ok || as.Execution == nil {
		t.Fatal(as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// This is the existing owner config mutation, not a fabricated run pin.
	cfg := testConfig()
	for i := range cfg.Hosts {
		if cfg.Hosts[i].HostID == "linux-1" {
			cfg.Hosts[i].Worker = protocol.WorkerOrca
		}
	}
	h.syncConfig(cfg)
	message := messageResult(t, h.exec(owner, h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: "Continue only on the original executor"}, rev(h.task(as.TaskID).ControlRevision)), SendRunMessage{}))
	if message.Status != protocol.RunMessageQueued {
		t.Fatal("host default replaced frozen run executor", message)
	}
	entry, err := h.app.NativeEntry(t.Context(), owner, as.RunID, "")
	if err != nil || entry.Worker != as.Execution.Worker || entry.Execution == nil || *entry.Execution != *as.Execution {
		t.Fatal(entry, err)
	}
}

func TestDirectIssueSourceCreatesItsOwnReviewDutyWithoutOwnerAuthority(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	// Finish the assignment notification before delivering the injected source.
	initial, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || initial.Operation != OpHandleEvent {
		t.Fatal(initial, ok, err)
	}
	first := startHostedReview(t, h, conversation, initial)
	manager := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + issue.IssueID, Thread: first.ThreadID, ConversationID: conversation}
	if op := hostedEffect(t, h, manager, first, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, manager, first, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, first)
	before := h.agentContext(issue.IssueID).Revision
	injected := h.exec(owner, h.env("conversation.inject_source", protocol.Target{Type: "message"}, protocol.InjectSourcePayload{ConversationID: "issue:" + issue.IssueID, Text: "An external report with unknown original author and time"}, nil), InjectSource{})
	var source protocol.ManualSource
	if injected.Status != protocol.OpAccepted || json.Unmarshal(injected.Result, &source) != nil {
		t.Fatal(injected)
	}
	if after := h.agentContext(issue.IssueID).Revision; after != before+1 {
		t.Fatal("explicit source did not change manager context", before, after)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || delivery.Operation != OpHandleMessage {
		t.Fatal(delivery, ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	if turn.MessageID != source.MessageID {
		t.Fatal("review did not admit the actual injected source", turn.MessageID, source.MessageID)
	}
	review := hostedReviewPayload(t, h, issue)
	found := false
	for _, s := range review.Sources {
		found = found || s.MessageID == source.MessageID
	}
	if !found {
		t.Fatal("external source lost its required manager review", review)
	}
	if op := hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, review, ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("tasks") != 0 || h.count("runs") != 0 || canonicalTaskIssue(t, h, issue.IssueID).Revision != issue.Revision {
		t.Fatal("evidence injection created execution or changed Issue contract")
	}
}
