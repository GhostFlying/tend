package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestForwardRecoveryLedgerFencesAdmissionAndPreservesTerminal(t *testing.T) {
	h := newHarness(t)
	original := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "old-201", ReleaseID: "old-release", Status: "completed", Phase: "completed", Revision: 8}, ManifestSHA256: strings.Repeat("a", 64), ServerHash: strings.Repeat("b", 64), ServerInstance: "server", ServerProcess: "old-launch", ServerHealthy: 1, ServerGrantDeadline: 1234567}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), original) }); err != nil {
		t.Fatal(err)
	}
	p := protocol.ForwardRecoveryPlan{RecoveryID: "forward-201", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), OriginalRequestID: h.ids.New(protocol.PrefixRequest), Original: protocol.UpgradeBinding{UpgradeID: original.View.UpgradeID, ReleaseID: original.View.ReleaseID, InstanceID: original.ServerInstance, ProcessInstanceID: original.ServerProcess, ManifestSHA256: original.ManifestSHA256, ArtifactSHA256: original.ServerHash}, OriginalLedger: original.View, LedgerRevision: 8}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		result, _ := json.Marshal(original.View)
		return tx.InsertRequest(t.Context(), store.RequestRecord{Domain: "system", RequestID: p.OriginalRequestID, Operation: "upgrades.start", PayloadHash: strings.Repeat("f", 64), PrincipalJSON: `{"kind":"system"}`, TraceID: h.ids.New(protocol.PrefixTrace), Status: protocol.OpSucceeded, ResultJSON: string(result)})
	}); err != nil {
		t.Fatal(err)
	}
	p.Successor = protocol.UpgradeBinding{UpgradeID: p.RecoveryID, InstanceID: "server", ProcessInstanceID: "new-launch", ReleaseID: "new-release", ArtifactSHA256: strings.Repeat("c", 64), ManifestSHA256: strings.Repeat("d", 64)}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: p.Successor.ProcessInstanceID, SHA256: p.Successor.ArtifactSHA256}
	h.app.forwardRecoveryID = p.RecoveryID
	if _, err := h.app.BeginOwnerMutation(t.Context()); err == nil {
		t.Fatal("startup fence allowed owner mutation before registration")
	}
	if paused, err := h.app.UpgradePausesMain(t.Context()); err != nil || !paused {
		t.Fatal("startup did not pause main", paused, err)
	}
	row, err := h.app.RegisterForwardRecovery(t.Context(), p, process)
	if err != nil || row.Linked {
		t.Fatal(row, err)
	}
	var snapshot store.UpgradeRow
	if err := json.Unmarshal(row.OriginalSnapshot, &snapshot); err != nil || !reflect.DeepEqual(snapshot, original) || len(row.OriginalSnapshotSHA256) != 64 {
		t.Fatal("public input did not preserve the full original ledger", err, snapshot)
	}
	if _, err := h.app.RegisterForwardRecovery(t.Context(), p, process); err != nil {
		t.Fatal("registration replay failed", err)
	}
	for _, command := range []Command{StartUpgrade{}, ControlUpgrade{}, HostMaintenance{}} {
		target := "upgrade"
		if command.Operation() == "hosts.maintenance" {
			target = "host"
		}
		_, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(command.Operation(), protocol.Target{Type: target, ID: "unrelated"}, map[string]any{}, rev(0)), command)
		if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
			t.Fatal("local owner system control bypassed fence", command.Operation(), err)
		}
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: 1}, true); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("local archive control bypassed fence", err)
	}
	proof := protocol.RecoveryReceipt{RecoveryID: p.RecoveryID, RequestID: p.RequestID, Original: p.Original, Successor: p.Successor, Process: process, Generation: 9, Epoch: "server-boot", FirstSequence: 1, SecondSequence: 2}
	// The receipt cannot release admission if its transaction fails, even after
	// the updater file has already durably committed the independent successor.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_forward_link BEFORE INSERT ON events WHEN NEW.kind='forward_recovery_linked' BEGIN SELECT RAISE(ABORT,'injected ledger failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.LinkForwardRecovery(t.Context(), proof); err == nil {
		t.Fatal("injected ledger transaction failure ignored")
	}
	row, _, err = h.app.ForwardRecoveryReadiness(t.Context(), p.RecoveryID)
	if err != nil || row.Linked {
		t.Fatal("failed linkage released fence", row, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_forward_link`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	row, err = h.app.LinkForwardRecovery(t.Context(), proof)
	if err != nil || !row.Linked {
		t.Fatal(row, err)
	}
	if _, err := h.app.LinkForwardRecovery(t.Context(), proof); err != nil {
		t.Fatal("link replay failed", err)
	}
	read, err := h.app.GetUpgrade(t.Context(), original.View.UpgradeID)
	if err != nil || !reflect.DeepEqual(read, original) {
		t.Fatal("terminal original was rewritten", read, err)
	}
	var count int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE kind='forward_recovery_linked'`).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatal("link event duplicated", count, err)
	}
	if done, err := h.app.BeginOwnerMutation(t.Context()); err != nil {
		t.Fatal(err)
	} else {
		done()
	}
}

func TestForwardRecoveryReplaysCommittedClaimButBlocksNewAdmission(t *testing.T) {
	h := newHarness(t)
	runtime := testRuntime("original-runtime")
	h.versionedHello("linux-1", runtime)
	h.createTask(owner, createTaskPayload{Title: "retained assignment", Spec: testSpec})
	payload := protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: runtime}
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, payload, nil)
	original, ok := h.claimWith("linux-1", env)
	if !ok {
		t.Fatal("fixture claim did not allocate work")
	}
	h.app.forwardRecoveryID = "claim-recovery"
	replayed, ok := h.claimWith("linux-1", env)
	if !ok || !reflect.DeepEqual(replayed, original) {
		t.Fatal("recovery changed a committed assignment receipt", replayed, original)
	}
	fresh := h.env(protocol.OpRunnerClaim, env.Target, payload, nil)
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), fresh, Claim{}); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("recovery admitted a new claim", err)
	}
	conflict := *env
	conflict.Payload = json.RawMessage(`{"different":"payload"}`)
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), &conflict, Claim{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("recovery hid a conflicting request identity", err)
	}
}

func TestForwardRecoveryAllowsOnlyIdentityBoundTerminalNativeFacts(t *testing.T) {
	h := newHarness(t)
	message, _ := h.say("existing native work")
	turn := store.MainTurn{RequestID: h.ids.New(protocol.PrefixRequest), MessageID: message, ThreadID: "retained-thread", TurnID: "retained-turn", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	h.app.forwardRecoveryID = "receipt-recovery"
	for _, tc := range []struct {
		name       string
		thread     string
		turn       string
		item       string
		status     protocol.AssistantState
		reconciled bool
	}{
		{name: "ordinary callback", thread: turn.ThreadID, turn: turn.TurnID, item: "item", status: protocol.AssistantCompleted},
		{name: "unrelated thread", thread: "other-thread", turn: turn.TurnID, item: "item", status: protocol.AssistantCompleted, reconciled: true},
		{name: "unrelated turn", thread: turn.ThreadID, turn: "other-turn", item: "item", status: protocol.AssistantCompleted, reconciled: true},
		{name: "missing item", thread: turn.ThreadID, turn: turn.TurnID, status: protocol.AssistantCompleted, reconciled: true},
		{name: "streaming output", thread: turn.ThreadID, turn: turn.TurnID, item: "item", status: protocol.AssistantStreaming, reconciled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: tc.thread}
			env := h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, recordAgentMessagePayload{Body: "not retained", ReplyTo: message, TurnID: tc.turn, ItemID: tc.item, Status: tc.status}, nil)
			if _, err := h.app.Execute(t.Context(), principal, env, RecordAgentMessage{Reconciled: tc.reconciled}); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				t.Fatal("non-reconciliation write crossed recovery fence", err)
			}
		})
	}
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: turn.ThreadID}
	env := h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, recordAgentMessagePayload{Body: "retained final answer", ReplyTo: message, TurnID: turn.TurnID, ItemID: "final-item", Status: protocol.AssistantCompleted}, nil)
	op, err := h.app.Execute(t.Context(), principal, env, RecordAgentMessage{Reconciled: true})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("retained terminal receipt could not reconcile", op, err)
	}
}
