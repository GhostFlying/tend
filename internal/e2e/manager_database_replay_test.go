package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestFault_LapsedManagerDatabaseReplayCommitsItsOriginalClose(t *testing.T) {
	lapsedManagerReplayCommitsOriginalClose(t, false)
}

func TestFault_LapsedManagerRepairReplayCommitsItsOriginalClose(t *testing.T) {
	lapsedManagerReplayCommitsOriginalClose(t, true)
}

func TestFault_ActiveClosingManagerRepairReplayCommitsItsOriginalClose(t *testing.T) {
	managerReplayCommitsOriginalClose(t, true, managerReplayOptions{active: true})
}

func TestFault_ActiveClosingManagerEmptyRepairReplayCommitsItsOriginalClose(t *testing.T) {
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
		body, _ := json.Marshal(payload)
		result, err := s.app.Execute(ctx, principal, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: body, ExpectedControlRevision: revision}, command)
		if err != nil || result.Status != protocol.OpSucceeded {
			t.Fatal(op, result, err)
		}
		return result
	}
	host := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}
	runtime := &protocol.RunnerRuntime{InstanceID: "database-replay-fixture", Generation: 1, Sequence: 1, Protocol: 2, ProtocolMin: 1, ProtocolMax: 2, Mode: protocol.RunnerNormal, JournalVersion: 13, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance, protocol.CapabilityAgentInventory}}
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
	execute(owner, protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Original responsibility"}, &issue.Revision, app.RequestIssueManager{})
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
	for sequence := int64(1); sequence <= 19; sequence++ {
		body := json.RawMessage(`{"thread_id":"original-native","native":{"Kind":"request_usage"}}`)
		if sequence >= 17 {
			text := fmt.Sprintf("part%d", sequence)
			if options.empty {
				text = ""
			}
			body = json.RawMessage(fmt.Sprintf(`{"thread_id":"original-native","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":"original-item","Text":%q}}`, text))
		}
		native(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: sequence, Body: body})
		if sequence < 17 {
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
	closeBody, _ := json.Marshal(protocol.ManagerNativeReply{ID: close, Result: json.RawMessage(`null`), LastSequence: 19})
	frozenClose := &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: closeBody}
	if _, err := s.app.Execute(ctx, principal, frozenClose, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("original frozen close bypassed unapplied deltas", err)
	}
	leaseStatus := "active"
	if !options.active {
		leaseStatus = "lapsed"
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
	cb := contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{})
	var pool *mainthread.BackgroundPool
	pool = &mainthread.BackgroundPool{App: s.app, New: func(id string) (*mainthread.Manager, error) {
		if drained, err := pool.UpgradeDrainStatus(ctx); err != nil || drained {
			t.Error("database replay admission was not serialized with drain", drained, err)
		}
		return mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, quiet()), mainthread.Config{Instance: "database-replay/" + id, ConversationID: id, Log: quiet(), OpenNamed: func(context.Context, hostedconfig.Selection) (harness.Session, error) {
			t.Error("closing database replay opened a native session or queried models")
			return nil, fmt.Errorf("native execution is forbidden")
		}}), nil
	}}
	run, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { pool.Run(run) })
	defer func() { cancel(); wg.Wait() }()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, as.RunID) != 0 {
		select {
		case <-deadline.C:
			t.Fatal("original closing deltas have no database consumer")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if s.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=?`, as.RunID) != 2 || s.count(`SELECT COUNT(*) FROM runs`) != 1 || s.count(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status=?`, as.RunID, leaseStatus) != 1 {
		t.Fatal("database replay queued native work, created a run or renewed the lease")
	}
	if repair {
		if s.count(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id='original-native' AND turn_id='original-turn'`) != 0 || s.count(`SELECT COUNT(*) FROM background_replies WHERE thread_id='original-native' AND turn_id='original-turn'`) != 0 {
			t.Fatal("internal repair text was published as a user reply")
		}
		if s.count(`SELECT COUNT(*) FROM events WHERE object_id=? AND kind='run.manager_repair_prefix_consumed'`, as.RunID) != 1 {
			t.Fatal("repair prefix has no durable internal consumption evidence")
		}
	} else if s.count(`SELECT COUNT(*) FROM conversation_agent_messages a JOIN conversation_messages m USING(message_id) WHERE a.thread_id='original-native' AND a.turn_id='original-turn' AND a.item_id='original-item' AND a.status='interrupted' AND m.body='part17part18part19'`) != 1 {
		t.Fatal("ordinary database replay lost original text")
	}
	if s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) != 1 || s.count(`SELECT COUNT(*) FROM main_turns WHERE request_id=? AND status='interrupted'`, business) != 1 || s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, as.RunID) != 19 {
		t.Fatal("prefix replay fabricated business completion or changed raw native events")
	}
	if options.retained {
		cancel()
		wg.Wait()
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
		native(protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original physical process exited", LastSequence: 19})
	}
	return databaseReplayFixture{server: s, assignment: as, runtime: runtime, business: business, instance: instance}
}
