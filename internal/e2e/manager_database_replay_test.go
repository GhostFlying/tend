package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_LapsedManagerLegacyTextAllowsOriginalClose(t *testing.T) {
	lapsedManagerReplayCommitsOriginalClose(t, false)
}

func TestFault_LapsedManagerLegacyRepairTextAllowsOriginalClose(t *testing.T) {
	lapsedManagerReplayCommitsOriginalClose(t, true)
}

func TestFault_ActiveManagerLegacyRepairTextAllowsOriginalClose(t *testing.T) {
	managerReplayCommitsOriginalClose(t, true, managerReplayOptions{active: true})
}

func TestFault_ActiveManagerLegacyEmptyTextAllowsOriginalClose(t *testing.T) {
	managerReplayCommitsOriginalClose(t, true, managerReplayOptions{active: true, empty: true})
}

type databaseReplayFixture struct {
	server             *server
	assignment         *protocol.Assignment
	runtime            *protocol.RunnerRuntime
	business, instance string
}

func lapsedManagerReplayCommitsOriginalClose(t *testing.T, repair bool, retained ...bool) databaseReplayFixture {
	t.Helper()
	return managerReplayCommitsOriginalClose(t, repair, managerReplayOptions{retained: len(retained) > 0 && retained[0]})
}

type managerReplayOptions struct{ active, empty, retained bool }

func managerReplayCommitsOriginalClose(t *testing.T, repair bool, options managerReplayOptions) databaseReplayFixture {
	t.Helper()
	s := newServer(t, time.Hour)
	ctx := t.Context()
	execute := func(principal protocol.Principal, op string, target protocol.Target, payload any, revision *int64, command app.Command) protocol.Operation {
		t.Helper()
		target, body := positiveFixtureInput(t, s.ids, op, target, payload)
		result, err := s.app.Execute(ctx, principal, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: body, ExpectedControlRevision: revision}, command)
		if err != nil || result.Status != protocol.OpSucceeded {
			t.Fatal(op, result, err)
		}
		return result
	}
	host := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}
	runtime := &protocol.RunnerRuntime{InstanceID: "database-replay-fixture", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.MinimumRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, JournalVersion: journal.CurrentVersion, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance, protocol.CapabilityAgentInventory}}
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
	created := execute(owner, "issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Reconcile the original manager", Project: "default", Intent: "Keep the original receipts"}}, nil, app.CanonicalOwnerIssueCommand{Action: "create"})
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
	claim := execute(host, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil, app.Claim{})
	var assignment struct{ Assignment *protocol.Assignment }
	if err := json.Unmarshal(claim.Result, &assignment); err != nil || assignment.Assignment == nil {
		t.Fatal(claim, err)
	}
	as := assignment.Assignment
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	selection, named, err := s.app.HostedRuntimeSelection(ctx, as.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	selection.AppliedModel = "frozen-model"
	business, source := s.ids.New(protocol.PrefixRequest), ""
	var manager store.IssueManagerTask
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		manager, err = store.GetIssueManagerTask(ctx, tx.Q(), as.TaskID)
		source = manager.SourceMessageID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if source == "" {
		submitted := s.exec(owner, "conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Original input"}, app.SubmitMessage{})
		var message struct {
			ID string `json:"message_id"`
		}
		if err := json.Unmarshal(submitted.Result, &message); err != nil {
			t.Fatal(err)
		}
		source = message.ID
	}
	if err := s.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: business, MessageID: source, ThreadID: "original-native", TurnID: "original-turn", Status: store.TurnInterrupted}, s.now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: business, ConversationID: as.Manager.ConversationID, IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: s.now().UnixMilli()}); err != nil {
			return err
		}
		if repair {
			logical := s.ids.New(protocol.PrefixRequest)
			if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: logical, MessageID: source, ThreadID: "original-native", Status: store.TurnInterrupted}, s.now().UnixMilli()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO logical_turns(logical_turn_id,message_id,status,repair_attempts,updated_at) VALUES(?,?,'pending_repair',1,?)`, logical, source, s.now().UnixMilli()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES(?,?,0)`, logical, logical); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES(?,?,1)`, business, logical); err != nil {
				return err
			}
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
	native := func(op string, payload any) protocol.Operation {
		return execute(principal, op, protocol.Target{Type: "run", ID: as.RunID}, payload, nil, app.ManagerNativeCommand{OperationName: op})
	}
	native(protocol.OpManagerNativeClaim, map[string]any{})
	native(protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: resume, Result: json.RawMessage(`{"ID":"original-native"}`)})
	const lastSequence = 60
	for sequence := int64(1); sequence <= lastSequence; sequence++ {
		body := json.RawMessage(`{"thread_id":"original-native","native":{"Kind":"request_usage"}}`)
		if sequence >= 47 {
			text := fmt.Sprintf("part%d", sequence)
			if options.empty {
				text = ""
			}
			body = json.RawMessage(fmt.Sprintf(`{"thread_id":"original-native","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":"original-item","Text":%q}}`, text))
		}
		native(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: sequence, Body: body})
		if sequence < 46 {
			if err := s.app.AcknowledgeManagerEvent(ctx, as.RunID, sequence, "original-native", "", "", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	close, err := s.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	native(protocol.OpManagerNativeClaim, map[string]any{})
	closeBody, _ := json.Marshal(protocol.ManagerNativeReply{ID: close, Result: json.RawMessage(`null`), LastSequence: lastSequence})
	frozenClose := &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: closeBody}
	if _, err := s.app.Execute(ctx, principal, frozenClose, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("original close bypassed an independent unapplied observation", err)
	}
	if !options.active {
		if err := s.store.Write(ctx, func(tx *store.Tx) error {
			if err := tx.UpdateRunObserved(ctx, as.RunID, protocol.RunUnknown, s.now().UnixMilli()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE leases SET status='lapsed' WHERE run_id=?`, as.RunID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	instance := "database-replay/" + as.Manager.ConversationID
	binding := selection
	binding.RunnerRunID, binding.ReceiptOnly = "", false
	binding.AppliedModel, binding.AppliedProvider, binding.AppliedEffort = "", "", ""
	raw, _ := json.Marshal(binding)
	sum := sha256.Sum256(raw)
	if err := s.app.SetMainThread(ctx, instance+"/named/"+hex.EncodeToString(sum[:]), "original-native"); err != nil {
		t.Fatal(err)
	}
	// After the original non-text event is applied, the same close can settle.
	// The 14 legacy fragments have their immutable original sequences and no
	// application dependency on a visible or buffered conversation item.
	if s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq BETWEEN 47 AND 60 AND applied=1`, as.RunID) != 14 {
		t.Fatal("legacy fragments still required a conversation acknowledgement")
	}
	if err := s.app.AcknowledgeManagerEvent(ctx, as.RunID, 46, "original-native", "", "", false); err != nil {
		t.Fatal(err)
	}
	if s.count(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id='original-native'`) != 0 ||
		s.count(`SELECT COUNT(*) FROM background_replies WHERE thread_id='original-native'`) != 0 ||
		s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) != 1 ||
		s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, as.RunID) != lastSequence {
		t.Fatal("fragment compatibility published text, settled an unrelated receipt or changed the cursor")
	}

	if options.retained {
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
		plan := protocol.CommittedMaintenancePlan{MaintenanceID: "receipt-reader-fixture", RequestID: s.ids.New(protocol.PrefixRequest), LinkRequestID: s.ids.New(protocol.PrefixRequest), PreparationRevision: prepare.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "receipt-reader-fixture", InstanceID: "server", ProcessInstanceID: "successor", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: as.TaskID, Run: protocol.RunView{RunID: as.RunID, Role: as.Role, Host: old.HostID, Generation: old.Generation, SpecVersion: 1, Observed: old.ObservedState}}}}}
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
	if result, err := s.app.Execute(ctx, principal, frozenClose, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); err != nil || result.Status != protocol.OpSucceeded {
		t.Fatal("same original close envelope could not commit after consumption", result, err)
	}
	if options.retained {
		native(protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original physical process exited", LastSequence: lastSequence})
	}
	return databaseReplayFixture{server: s, assignment: as, runtime: runtime, business: business, instance: instance}
}
