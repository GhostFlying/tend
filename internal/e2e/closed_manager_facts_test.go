package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
)

// The close and independent stopped facts are controlled durable fixture input,
// not evidence that an OS process exited. The real App and RPC authenticate and
// reconcile their original envelopes; no native executor is installed here.
func TestFault_ClosedRepairToolFactsReconcileOriginalHTTPReceipts(t *testing.T) {
	closedRepairToolFacts(t, closedManagerRecoveryOptions{})
}

func TestFault_ManagerStoppedHTTPRecoveryReleasesOriginalReceiptReader(t *testing.T) {
	closedRepairToolFacts(t, closedManagerRecoveryOptions{runner: true})
}

func TestFault_ManagerStoppedSettledCloseHTTPRecovery(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		name := "normal"
		if bounded {
			name = "bounded"
		}
		t.Run(name, func(t *testing.T) {
			closedRepairToolFacts(t, closedManagerRecoveryOptions{runner: true, settled: true, bounded: bounded})
		})
	}
}

type closedManagerRecoveryOptions struct{ runner, settled, bounded, normal bool }

func closedRepairToolFacts(t *testing.T, options closedManagerRecoveryOptions) {
	t.Helper()
	ctx := t.Context()
	s := newServer(t, time.Hour)
	execute := func(principal protocol.Principal, operation string, target protocol.Target, payload any, revision *int64, command app.Command) protocol.Operation {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		op, err := s.app.Execute(ctx, principal, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: revision}, command)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatalf("%s: status=%s error=%v operation_error=%v", operation, op.Status, err, op.Error)
		}
		return op
	}
	host := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}
	runtime := &protocol.RunnerRuntime{InstanceID: "closed-facts-fixture", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, JournalVersion: journal.CurrentVersion, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance, protocol.CapabilityAgentInventory, protocol.CapabilityRetainedReconciliation}}
	execute(host, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil, app.Hello{})
	zero := int64(0)
	execute(owner, "categories.put", protocol.Target{Type: "category", ID: protocol.DefaultIssueManagerCategory}, protocol.PutAgentCategoryPayload{Title: "Managers", State: "active"}, &zero, app.PutAgentCategory{})
	definitions, err := agentconfig.Validate("linux-1", agentconfig.Config{FormatVersion: 2, Runtimes: []agentconfig.RuntimeBinding{{ID: "native-manager", Revision: 1, Kind: agentconfig.AppServerWorker, CLI: "codex", ExecutableRef: "fixture-native", DeclaredPurposes: []agentconfig.Purpose{agentconfig.IssueManager}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "manager", Revision: 1, DisplayName: "Manager", RuntimeID: "native-manager", Categories: []string{protocol.DefaultIssueManagerCategory}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "frozen-model", Enabled: true}}}, agentconfig.SourceReference{Kind: agentconfig.Inline})
	if err != nil {
		t.Fatal(err)
	}
	projection := definitions.Public()
	_, hash, err := definitions.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	execute(host, protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: "linux-1"}, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: "frozen-manager", PublicSHA256: hash, Projection: projection}, &zero, app.RegisterAgentInventory{})
	pin := protocol.RunExecutionPin{Purpose: "issue_manager", Identity: projection.Agents[0].Identity, AgentRevision: 1, Runtime: projection.Agents[0].Binding, InventoryRevision: 1, SnapshotID: "frozen-manager", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: "frozen-model"}, Worker: protocol.WorkerAppServer}
	runtime.Sequence++
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityManagerReceiptReader)
	runtime.AgentExecutors = []protocol.AgentExecutor{{Pin: pin, Available: true}}
	execute(host, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil, app.Hello{})
	created := execute(owner, "issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Reconcile original tool metadata", Project: "default", Intent: "Preserve the original unknown business result"}}, nil, app.CanonicalOwnerIssueCommand{Action: "create"})
	var issue protocol.Issue
	if err := json.Unmarshal(created.Result, &issue); err != nil {
		t.Fatal(err)
	}
	selected := execute(owner, protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Original responsibility"}, &issue.Revision, app.RequestIssueManager{})
	if err := json.Unmarshal(selected.Result, &issue); err != nil {
		t.Fatal(err)
	}
	admitted := execute(owner, "issues.set_status", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueTodo}, &issue.Revision, app.CanonicalOwnerIssueCommand{Action: "set_status"})
	if err := json.Unmarshal(admitted.Result, &issue); err != nil {
		t.Fatal(err)
	}
	claimed := execute(host, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil, app.Claim{})
	var claim struct{ Assignment *protocol.Assignment }
	if err := json.Unmarshal(claimed.Result, &claim); err != nil || claim.Assignment == nil {
		t.Fatal(claimed, err)
	}
	as := *claim.Assignment
	as.RunToken = runnerapi.RunToken(secret, as.LeaseID)
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	target := protocol.Target{Type: "run", ID: as.RunID}
	native := func(operation string, payload any) protocol.Operation {
		return execute(principal, operation, target, payload, nil, app.ManagerNativeCommand{OperationName: operation})
	}
	selection, named, err := s.app.HostedRuntimeSelection(ctx, as.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	selection.AppliedModel = "frozen-model"
	var manager store.IssueManagerTask
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		manager, err = store.GetIssueManagerTask(ctx, tx.Q(), as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if manager.SourceMessageID == "" {
		submitted := s.exec(owner, "conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Original repair input"}, app.SubmitMessage{})
		var source struct {
			ID string `json:"message_id"`
		}
		if err := json.Unmarshal(submitted.Result, &source); err != nil {
			t.Fatal(err)
		}
		manager.SourceMessageID = source.ID
	}
	business, logical := s.ids.New(protocol.PrefixRequest), s.ids.New(protocol.PrefixRequest)
	const thread, turn, callID = "original-native", "original-repair-turn", "original-call"
	if err := s.store.Write(ctx, func(tx *store.Tx) error {
		for _, rec := range []store.MainTurn{{RequestID: logical, MessageID: manager.SourceMessageID, ThreadID: thread, Status: store.TurnInterrupted}, {RequestID: business, MessageID: manager.SourceMessageID, ThreadID: thread, TurnID: turn, Status: store.TurnInterrupted}} {
			if err := tx.PutMainTurn(ctx, rec, s.now().UnixMilli()); err != nil {
				return err
			}
		}
		if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: business, ConversationID: as.Manager.ConversationID, IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: s.now().UnixMilli()}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO logical_turns(logical_turn_id,message_id,status,repair_attempts,updated_at) VALUES(?,?,'pending_repair',1,?)`, logical, manager.SourceMessageID, s.now().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES(?,?,0),(?,?,1)`, logical, logical, business, logical); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, business)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resume, err := s.app.QueueManagerNative(ctx, as.RunID, "resume", json.RawMessage(`{"thread_id":"original-native"}`))
	if err != nil {
		t.Fatal(err)
	}
	native(protocol.OpManagerNativeClaim, map[string]any{})
	native(protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: resume, Result: json.RawMessage(`{"ID":"original-native"}`)})
	arguments := json.RawMessage(`{"context_revision":1}`)
	result := harness.ToolResult{Success: true, Text: "Original context review acknowledged"}
	call := coordination.Event{ThreadID: thread, Call: &harness.ToolCall{ThreadID: thread, TurnID: turn, CallID: callID, Tool: "issues_context_review", Arguments: arguments}}
	callBody, _ := json.Marshal(call)
	native(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: callBody})
	answer, err := s.app.QueueManagerNative(ctx, as.RunID, "tool_result", mustClosedFactsJSON(t, coordination.Request{ThreadID: thread, TurnID: turn, CallID: callID, ToolResult: result}))
	if err != nil {
		t.Fatal(err)
	}
	native(protocol.OpManagerNativeClaim, map[string]any{})
	native(protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: answer, Result: json.RawMessage(`null`)})
	eventBodies := []json.RawMessage{callBody}
	for i, status := range []string{"inProgress", "completed"} {
		kind, toolStatus, output := harness.EventItemStarted, protocol.ToolRunning, ""
		raw := map[string]any{"type": "dynamicToolCall", "id": callID, "tool": "issues_context_review", "arguments": arguments, "status": status}
		if i == 1 {
			kind, toolStatus, output = harness.EventItemCompleted, protocol.ToolCompleted, result.Text
			raw["success"] = result.Success
			raw["contentItems"] = []map[string]string{{"type": "inputText", "text": result.Text}}
		}
		body := mustClosedFactsJSON(t, coordination.Event{ThreadID: thread, Native: &harness.Event{Kind: kind, TurnID: turn, ItemID: callID, ItemType: "dynamicToolCall", Tool: &harness.ToolUse{Name: "issues_context_review", Input: string(arguments), Output: output, Status: toolStatus}, Raw: mustClosedFactsJSON(t, raw)}})
		eventBodies = append(eventBodies, body)
		native(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: int64(i + 2), Body: body})
	}
	closeID, err := s.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !options.normal {
		native(protocol.OpManagerNativeClaim, map[string]any{})
		if err := s.store.Write(ctx, func(tx *store.Tx) error {
			if err := tx.UpdateRunObserved(ctx, as.RunID, protocol.RunUnknown, s.now().UnixMilli()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE leases SET status='lapsed' WHERE run_id=?`, as.RunID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		prepare, err := s.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: s.ids.New(protocol.PrefixRequest)}, true)
		if err != nil {
			t.Fatal(err)
		}
		var old store.RunRow
		if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			old, err = store.GetRun(ctx, tx.Q(), as.RunID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		plan := protocol.CommittedMaintenancePlan{MaintenanceID: "closed-facts-fixture", RequestID: s.ids.New(protocol.PrefixRequest), LinkRequestID: s.ids.New(protocol.PrefixRequest), PreparationRevision: prepare.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "closed-facts-fixture", InstanceID: "server", ProcessInstanceID: "successor", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: as.TaskID, Run: protocol.RunView{RunID: as.RunID, Role: as.Role, Host: old.HostID, Generation: old.Generation, SpecVersion: 1, Observed: old.ObservedState}}}}}
		process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "successor", SHA256: plan.Successor.ArtifactSHA256}
		if _, err := s.app.RegisterCommittedMaintenance(ctx, plan, process); err != nil {
			t.Fatal(err)
		}
		if _, err := s.app.LinkCommittedMaintenance(ctx, protocol.CommittedMaintenanceReceipt{MaintenanceID: plan.MaintenanceID, RequestID: plan.RequestID, Previous: plan.Previous, Successor: plan.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: s.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prepare.Revision}, false); err != nil {
			t.Fatal(err)
		}
	}
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), s.now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, thread); err != nil {
		t.Fatal(err)
	}
	if !options.normal {
		if _, err := j.NextAttempt(ctx, as.RunID); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range eventBodies {
		env := protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeEvent, Target: target}
		if err := j.SaveManagerEvent(ctx, as.RunID, &env, body); err != nil {
			t.Fatal(err)
		}
		if err := j.DeleteRequest(ctx, env.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	if options.normal {
		recoverStoppedManagerHTTP(t, s, j, as, runtime, protocol.Envelope{}, protocol.Envelope{}, business, turn, options)
		return
	}
	if _, _, err := j.ManagerOperation(ctx, as.RunID, protocol.ManagerNativeRequest{ID: closeID, Method: "close", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	closed := protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`), LastSequence: 3}
	if err := j.CompleteManagerOperation(ctx, closed); err != nil {
		t.Fatal(err)
	}
	closeEnvelope := protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeReply, Target: target, Payload: mustClosedFactsJSON(t, closed)}
	if err := j.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: closeEnvelope}); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	stopped := protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeStopped, Target: target, Payload: mustClosedFactsJSON(t, protocol.ManagerStoppedPayload{Reason: "Controlled original process stopped fact", LastSequence: 3})}
	if err := j.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: stopped}); err != nil {
		t.Fatal(err)
	}
	if options.runner {
		recoverStoppedManagerHTTP(t, s, j, as, runtime, closeEnvelope, stopped, business, turn, options)
		return
	}
	if _, err := j.NextProcessGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := j.BoundedDataIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	authority := protocol.BoundedMaintenanceAuthorization{RequestID: "controlled-original-force", Role: "runner", DataIdentity: &data, Target: protocol.UpgradeBinding{ProcessInstanceID: "controlled-successor"}}
	if err := j.QuarantineBoundedRecovery(ctx, authority); err != nil {
		t.Fatal(err)
	}
	if err := j.ReleaseBoundedAdmission(ctx, protocol.BoundedRunnerAdmissionRelease{RequestID: "controlled-admission-release", RecoveryAuthorizationID: authority.RequestID, Successor: authority.Target, Journal: data}); err != nil {
		t.Fatal(err)
	}
	frozen, err := j.BoundedRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := j.Run(ctx, as.RunID)
	if err != nil || !before.Fenced || !before.StoppedConfirmed || before.Attempts != 1 {
		t.Fatal(before, err)
	}
	requestBefore := s.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=?`, as.RunID)
	var answerBefore string
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT payload_json||result_json FROM manager_native_requests WHERE request_id=?`, answer).Scan(&answerBefore)
	}); err != nil {
		t.Fatal(err)
	}
	for _, envelope := range []protocol.Envelope{closeEnvelope, stopped} {
		protected, err := j.BoundedRequestQuarantined(ctx, envelope.RequestID)
		if err != nil || !protected {
			t.Fatal("original factual envelope escaped its frozen inventory", protected, err)
		}
		op := sendClosedFactsEnvelope(t, s, as, envelope)
		if err := j.AcknowledgeBoundedFinal(ctx, journal.Request{RunID: as.RunID, Envelope: envelope}, op); err != nil {
			t.Fatal(err)
		}
		if again := sendClosedFactsEnvelope(t, s, as, envelope); !reflect.DeepEqual(again, op) {
			t.Fatal("original envelope retry changed its saved acknowledgement", op, again)
		}
	}
	after, err := j.Run(ctx, as.RunID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("fact acknowledgement resumed or changed the original attempt", after, err)
	}
	quarantine, err := j.BoundedRecovery(ctx)
	if err != nil || !reflect.DeepEqual(frozen, quarantine) {
		t.Fatal("fact acknowledgement changed quarantine or its admission release", err)
	}
	if requests, err := j.Requests(ctx); err != nil || len(requests) != 0 {
		t.Fatal("original terminal envelopes remain pending", requests, err)
	}
	if s.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=?`, as.RunID) != requestBefore || s.count(`SELECT COUNT(*) FROM runs`) != 1 || s.count(`SELECT COUNT(*) FROM main_turns`) != 2 || s.count(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status='lapsed'`, as.RunID) != 1 {
		t.Fatal("receipt reconciliation created native work, a run, turn or lease")
	}
	if s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) != 1 || s.count(`SELECT COUNT(*) FROM main_turns WHERE request_id=? AND status='interrupted'`, business) != 1 || s.count(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id=?`, thread) != 0 || s.count(`SELECT COUNT(*) FROM conversation_tool_uses WHERE thread_id=?`, thread) != 0 {
		t.Fatal("physical receipts settled business unknown or published repair output")
	}
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var saved string
		if err := tx.Q().QueryRowContext(ctx, `SELECT payload_json||result_json FROM manager_native_requests WHERE request_id=?`, answer).Scan(&saved); err != nil {
			return err
		}
		if saved != answerBefore {
			return fmt.Errorf("original tool result changed")
		}
		for i, original := range eventBodies {
			var raw string
			var applied bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT body_json,applied FROM manager_native_events WHERE run_id=? AND seq=?`, as.RunID, i+1).Scan(&raw, &applied); err != nil {
				return err
			}
			if raw != string(original) || !applied {
				return fmt.Errorf("original event %d was changed or left unconsumed", i+1)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proof, err := s.app.ManagerRunReconciliation(ctx, as.TaskID, as.RunID)
	if err != nil || !slices.Equal(proof.Blockers, []string{"business_receipts_unreconciled"}) || proof.CloseRequestID != closeID || proof.StoppedRequestID != stopped.RequestID || proof.EventLastSequence != 3 || proof.CloseLastSequence != 3 || proof.StoppedLastSequence != 3 || !proof.PendingBusinessReceipts || proof.Observed != protocol.RunUnknown || proof.AssignmentEpoch != manager.AssignmentEpoch {
		t.Fatal("original physical receipts did not leave only original business reconciliation", proof, err)
	}
	t.Logf("original close/stopped ACK: cursor=%d attempt=%d; raw events and unresolved business receipt retained; no native executor installed", proof.EventLastSequence, after.Attempts)
}

func mustClosedFactsJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sendClosedFactsEnvelope(t *testing.T, s *server, as protocol.Assignment, envelope protocol.Envelope) protocol.Operation {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/runner/v1/rpc", bytes.NewReader(mustClosedFactsJSON(t, envelope)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+as.RunToken)
	request.Header.Set(runnerapi.HeaderRun, as.RunID)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var reply struct {
		Operation *protocol.Operation `json:"operation"`
		Error     *protocol.Error     `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || reply.Error != nil || reply.Operation == nil || reply.Operation.Status != protocol.OpSucceeded || reply.Operation.RequestID != envelope.RequestID {
		t.Fatalf("original %s did not reconcile through the real App HTTP handler: status=%d error=%v operation=%+v", envelope.Operation, response.StatusCode, reply.Error, reply.Operation)
	}
	return *reply.Operation
}
