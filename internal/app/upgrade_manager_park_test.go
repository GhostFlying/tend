package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"reflect"
	"strings"
	"testing"
	"time"
)

func parkUpgradeFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Assignment, StartUpgrade) {
	t.Helper()
	h, runtime, _, as, _ := reviewManagerAssignment(t)
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, second)
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		rt := testRuntime("upgrade-" + host)
		if host == "linux-1" {
			rt = runtime
			rt.Sequence++
		}
		rt.JournalVersion = runtime.JournalVersion
		rt.Capabilities = append(rt.Capabilities, capabilityUpdater)
		rt.Build.BinarySHA256 = strings.Repeat("a", 64)
		rt.Build.Version = "old"
		if op := h.versionedHello(host, rt); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	var schema int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
	}); err != nil {
		t.Fatal(err)
	}
	c := StartUpgrade{ServerCapabilities: []string{release.ServerCapabilityManagerPark}, ParkIdleManagers: true, Manifest: release.Manifest{Format: 1, ReleaseID: "release-new", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 27, Max: schema}, RunnerJournal: release.Range{Min: 9, Max: runtime.JournalVersion}}, Artifacts: []release.Artifact{{Role: "server", OS: "linux", Arch: "amd64", Path: "server", Size: 20, SHA256: strings.Repeat("c", 64)}, {Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: 10, SHA256: strings.Repeat("b", 64)}}}, ManifestSHA256: strings.Repeat("d", 64), ManifestSize: 128, ServerHash: strings.Repeat("c", 64), ServerInstance: "server"}
	return h, runtime, as, c
}

func TestUpgradeManagerParkClosesBeforeDrainAndResumesNewRun(t *testing.T) {
	h, runtime, as, c := parkUpgradeFixture(t)
	if op := startTestUpgrade(h, "park-one", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	if len(batch.ManagerParks) != 1 || batch.ManagerParks[0].RunID != as.RunID {
		t.Fatal(batch)
	}
	if op := startTestUpgrade(h, "park-two", c); op.Status != protocol.OpFailed {
		t.Fatal("second window", op)
	}
	// Start was accepted without synchronously issuing native close or finishing self.
	if h.count("manager_native_requests") != 0 || h.run(as.RunID).ObservedState == protocol.RunFinished {
		t.Fatal("synchronous self close")
	}
	upgradeTick(h, ServerObservation{MainDrained: false})
	batch = upgradeRead(h)
	if batch.View.Phase != "server_draining" || batch.View.ManagerParks[0].Phase != "closing" {
		t.Fatal(batch)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("new business crossed fence", err)
	}
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var close protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &close) != nil || close.ID != batch.ManagerParks[0].CloseRequestID || close.Method != "close" {
		t.Fatal(op, close)
	}
	op = h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: close.ID, Result: json.RawMessage(`null`), LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if op.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFinished || h.task(as.TaskID).Lifecycle != protocol.TaskWaiting {
		t.Fatal(op, h.run(as.RunID), h.task(as.TaskID))
	}
	// New owner input is durable, but cannot reacquire the manager under this window.
	submitHostedAdmissionInput(t, h, as.Manager.ConversationID)
	if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{}); op.Status != protocol.OpSucceeded || strings.Contains(string(op.Result), `"run_id"`) {
		t.Fatal(op)
	}
	batch = upgradeRead(h)
	op = h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	ack := upgradeAck(h, "linux-1", protocol.RunnerNormal)
	next := reviewClaimManager(t, h, &ack)
	if next.RunID == as.RunID || next.LeaseID == as.LeaseID || next.TaskID != as.TaskID || next.Generation <= as.Generation {
		t.Fatal("not a new continuation", as, next)
	}
}

func TestUpgradeManagerParkUnknownDoesNotQueueClose(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := startTestUpgrade(h, "park-unknown", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true})
	if h.count("manager_native_requests") != 0 || h.run(as.RunID).ObservedState != protocol.RunUnknown || upgradeRead(h).View.Phase != "server_draining" {
		t.Fatal("unknown fabricated stop")
	}
}

func parkCloseForTest(t *testing.T, h *harness, as protocol.Assignment) {
	t.Helper()
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var request protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &request) != nil || request.Method != "close" {
		t.Fatal(op, request)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: request.ID, Result: json.RawMessage(`null`), LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestUpgradeManagerParkCompletesServerUpgradeBeforeContinuation(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	// This release replaces the server and exactly retains both installed runner
	// artifacts. Normal matching-version guards still decide whether to skip.
	c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
	if op := startTestUpgrade(h, "park-success", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: false})
	parkCloseForTest(t, h, as)
	submitHostedAdmissionInput(t, h, as.Manager.ConversationID)
	if v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)}); v.Phase != "server_ready" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	binding := protocol.UpgradeBinding{UpgradeID: "park-success", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "new-park-server"}
	grant, err := h.app.ServerUpgradeAuthorize(t.Context(), binding)
	if err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	observed := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: binding.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true, MainDrained: true}
	upgradeTick(h, observed)
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, observed); v.Phase != "runners" {
		t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	for i := 0; i < 4; i++ {
		upgradeTick(h, observed)
	}
	if batch := upgradeRead(h); batch.View.Status != "completed" || batch.ServerProcess != binding.ProcessInstanceID {
		t.Fatal(batch)
	}
	ack := upgradeAck(h, "linux-1", protocol.RunnerNormal)
	next := reviewClaimManager(t, h, &ack)
	if next.RunID == as.RunID || next.TaskID != as.TaskID || next.LeaseID == as.LeaseID || next.Manager.ConversationID != as.Manager.ConversationID {
		t.Fatal(as, next)
	}
}

func TestUpgradeManagerParkWaitsForActualInitiatingTurn(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	issue := canonicalTaskIssue(t, h, as.Manager.IssueID)
	// The explicit owner's original delegation is also queued for the main
	// coordinator. Record its real admission before the child reviews that source.
	mainDelivery, _, admitted, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !admitted {
		t.Fatal(admitted, err)
	}
	var original HandleMessage
	if err := json.Unmarshal(mainDelivery.Payload, &original); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: mainDelivery.RequestID, MessageID: original.MessageID, ThreadID: mainAgent.Thread, TurnID: "owner-delegation", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, as.Manager.ConversationID, delivery)
	if op := startTestUpgrade(h, "park-active-turn", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: false})
	if h.count("manager_native_requests") != 0 {
		t.Fatal("active initiating turn was closed synchronously")
	}
	principal := runPrincipal(as.RunID, protocol.RoleIssueManager)
	principal.Thread, principal.ConversationID = turn.ThreadID, as.Manager.ConversationID
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, principal, turn, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, ServerObservation{MainDrained: false})
	if batch := upgradeRead(h); batch.View.ManagerParks[0].Phase != "closing" {
		t.Fatal(batch)
	}
}

func TestUpgradeManagerParkFailedCloseKeepsOriginalResponsibility(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	if op := startTestUpgrade(h, "park-unknown-close", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: false})
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var close protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &close) != nil || close.Method != "close" {
		t.Fatal(op)
	}
	op = h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: close.ID, Error: "Physical close outcome requires independent reconciliation"}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for i := 0; i < 3; i++ {
		upgradeTick(h, ServerObservation{MainDrained: true})
	}
	if run := h.run(as.RunID); run.ObservedState != protocol.RunRunning || h.count("manager_native_requests") != 1 || upgradeRead(h).View.Phase != "server_draining" {
		t.Fatal(run, upgradeRead(h))
	}
	h.clock.Advance(31 * time.Minute)
	batch := upgradeTick(h, ServerObservation{MainDrained: true})
	if batch.Status != "attention" || h.run(as.RunID).ObservedState != protocol.RunRunning {
		t.Fatal(batch)
	}
}

func TestUpgradeManagerParkCancelledUnknownClosePreservesRequest(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	if op := startTestUpgrade(h, "review-first", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var close protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &close) != nil {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: close.ID, Error: "Native coordinator operation failed"}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	if op := startTestUpgrade(h, "review-second", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	if count := h.count("manager_native_requests"); count != 1 {
		t.Fatalf("unreconciled close %s was repeated under new close ID %s; requests=%d", close.ID, upgradeRead(h).ManagerParks[0].CloseRequestID, count)
	}
}

func TestUpgradeManagerParkTerminalUnknownNativeRequestBlocksDrain(t *testing.T) {
	h, runtime, as, c := parkUpgradeFixture(t)
	runtime.Sequence++
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityNativeUsage)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "fork", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var native protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &native) != nil || native.ID != id {
		t.Fatal(op)
	}
	if op := startTestUpgrade(h, "review-unknown-start", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	op = h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Confirmed native exit while original start is unknown", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var status string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE request_id=?`, id).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" {
		t.Fatalf("expected retained unknown, got %s", status)
	}
	v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	if v.Phase != "server_draining" {
		t.Fatalf("advanced despite retained native request %s=%s: phase=%s park=%+v", id, status, v.Phase, v.ManagerParks)
	}
}

func TestUpgradeManagerParkRestartAllowsOnlyCapturedReceiptAttachment(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	if op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := startTestUpgrade(h, "review-restart", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var close protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &close) != nil {
		t.Fatal(op)
	}
	_, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, protocol.RoleIssueManager), h.env(protocol.OpManagerNativeReply, protocol.Target{Type: "run", ID: as.RunID}, protocol.ManagerNativeReply{ID: close.ID, Result: json.RawMessage(`null`), LastSequence: 1}, nil), ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("expected unapplied-event blocker", err)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	conversations, err := restarted.PendingAgentConversations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range conversations {
		if id == as.Manager.ConversationID {
			found = true
		}
	}
	if !found {
		t.Fatal("original receipt attachment disappeared", conversations)
	}
	selected, named, err := restarted.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !named || !selected.ReceiptOnly {
		t.Fatal(selected, named, err)
	}

	allowed, err := restarted.UpgradeParkReceiptRead(t.Context(), as.Manager.ConversationID, as.RunID)
	if err != nil || !allowed {
		t.Fatal("exact original receipt attachment denied", allowed, err)
	}
	for _, entry := range []struct{ conversation, run string }{{as.Manager.ConversationID, "unrelated-run"}, {"other-conversation", as.RunID}, {as.Manager.ConversationID, ""}} {
		if allowed, err := restarted.UpgradeParkReceiptRead(t.Context(), entry.conversation, entry.run); err != nil || allowed {
			t.Fatal("unrelated attachment admitted", entry, allowed, err)
		}
	}
	batch := upgradeRead(h)
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The rejected reply did not resolve the unapplied event or commit close.
	// Cancelling the window cannot discard the original physical-stop proof gap.
	var closeStatus string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE request_id=?`, close.ID).Scan(&closeStatus)
	}); err != nil || closeStatus != "started" {
		t.Fatal("original close no longer requires reconciliation", closeStatus, err)
	}
	if allowed, err := restarted.UpgradeParkReceiptRead(t.Context(), as.Manager.ConversationID, as.RunID); err != nil || !allowed {
		t.Fatal("cancelled window lost original unresolved receipt authority", allowed, err)
	}
}

func TestUpgradeManagerParkPreflightPreservesExactRecordedRequest(t *testing.T) {
	h, _, _, c := parkUpgradeFixture(t)
	env := h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "park-preflight"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0))
	original := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, c)
	if original.Status != protocol.OpSucceeded {
		t.Fatal(original)
	}
	recorded, err := h.app.CheckUpgradeStart(t.Context(), env, c)
	if err != nil || recorded == nil || string(recorded.Result) != string(original.Result) {
		t.Fatal(recorded, err)
	}
	other := *env
	other.Payload = json.RawMessage(`{"release_id":"changed"}`)
	if _, err := h.app.CheckUpgradeStart(t.Context(), &other, c); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("changed original payload accepted", err)
	}
	c.DeliveryTaskID = "unapproved-delivery"
	env = h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: "new-unapproved"}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0))
	if _, err := h.app.CheckUpgradeStart(t.Context(), env, c); err == nil {
		t.Fatal("unapproved source passed pre-execution authority")
	}
}

func TestUpgradeManagerParkBusyRestartRestoresOriginalReceiptOnlySelection(t *testing.T) {
	h, _, as, c := parkUpgradeFixture(t)
	mainDelivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var original HandleMessage
	if err := json.Unmarshal(mainDelivery.Payload, &original); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: mainDelivery.RequestID, MessageID: original.MessageID, ThreadID: mainAgent.Thread, TurnID: "owner-delegation", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, as.Manager.ConversationID, delivery)
	if op := startTestUpgrade(h, "busy-attachment-restart", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	restarted := New(h.store, h.ids, h.clock.Now)
	selected, named, err := restarted.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !named || !selected.RunnerManaged || !selected.ReceiptOnly || selected.RunnerRunID != as.RunID {
		t.Fatal("busy original cannot attach read-only", selected, named, err)
	}
	if allowed, err := restarted.UpgradeParkReceiptRead(t.Context(), as.Manager.ConversationID, as.RunID); err != nil || !allowed {
		t.Fatal(allowed, err)
	}
	if h.count("manager_native_requests") != 0 || upgradeRead(h).View.Phase != "server_draining" {
		t.Fatal("started turn was physically closed", turn)
	}
	if _, err := restarted.QueueManagerNative(t.Context(), as.RunID, "start", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("receipt attachment acquired start permission", err)
	}
}

func TestUpgradeManagerParkSerializesAllAttachmentClosesWithWindow(t *testing.T) {
	h, runtime, as, c := parkUpgradeFixture(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityNativeUsage)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := startTestUpgrade(h, "teardown-owned-by-window", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	original := upgradeRead(h).ManagerParks[0].CloseRequestID
	// Even a genuine Session.Close cannot overtake the idle/receipt admission.
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition || h.count("manager_native_requests") != 0 {
		t.Fatal("teardown bypassed park admission", err)
	}
	upgradeTick(h, ServerObservation{})
	for range 2 {
		id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{"config":{},"input":{},"tool_result":{}}`))
		if err != nil || id != original || h.count("manager_native_requests") != 1 {
			t.Fatal("attachment queued a competing close", id, err)
		}
	}
	op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var close protocol.ManagerNativeRequest
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &close) != nil || close.ID != original {
		t.Fatal(op, close)
	}
	failed := protocol.ManagerNativeReply{ID: original, Result: json.RawMessage(`null`), Error: "Native close result is uncertain"}
	if op := h.runAs(as, protocol.OpManagerNativeReply, failed, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A non-rejected failure remains the original uncertain result even after
	// cancellation and recreation of the controller or the upgrade window.
	for _, cancel := range []bool{false, true} {
		if cancel {
			batch := upgradeRead(h)
			if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
		restarted := New(h.store, h.ids, h.clock.Now)
		id, err := restarted.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
		if err != nil || id != original || h.count("manager_native_requests") != 1 {
			t.Fatal("unknown close repeated", cancel, id, err)
		}
		result, ready, err := restarted.ManagerNativeResult(t.Context(), as.RunID, id)
		if err != nil || !ready || !reflect.DeepEqual(result, failed) {
			t.Fatal("original close fact changed", result, err)
		}
	}
	// The window-wide fence is gone after cancellation, but the exact original
	// uncertain close still fences every new native business operation.
	restarted := New(h.store, h.ids, h.clock.Now)
	for _, method := range []string{"start", "fork", "turn"} {
		if _, err := restarted.QueueManagerNative(t.Context(), as.RunID, method, json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("cancelled unknown close admitted original business", method, err)
		}
	}
	selected, named, err := restarted.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !named || !selected.ReceiptOnly || selected.RunnerRunID != as.RunID {
		t.Fatal("cancelled unknown close lost receipt-only identity", selected, named, err)
	}
	if allowed, err := restarted.UpgradeParkReceiptRead(t.Context(), as.Manager.ConversationID, as.RunID); err != nil || !allowed {
		t.Fatal("original read-only attachment lost", allowed, err)
	}
	// A durable business command queued before the physical uncertainty must
	// also be rejected at claim time, rather than handed to the native driver.
	late := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'turn','{}','pending',?)`, late, as.RunID, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claimed := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if claimed.Status != protocol.OpSucceeded || !strings.Contains(string(claimed.Result), `"request":null`) {
		t.Fatal("late native business claimed after cancellation", claimed)
	}
	result, ready, err := restarted.ManagerNativeResult(t.Context(), as.RunID, late)
	if err != nil || !ready || !result.Rejected || result.Error == "" {
		t.Fatal("late rejected fact lost", result, ready, err)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	if op := startTestUpgrade(h, "teardown-following-window", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	if upgradeRead(h).ManagerParks[0].CloseRequestID != original || h.count("manager_native_requests") != 2 || h.run(as.RunID).ObservedState == protocol.RunFinished {
		t.Fatal("new window repeated unknown or fabricated completion")
	}
}

func TestUpgradeManagerParkCancelRestoresOnlyUnattemptedOrRejectedClose(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattempted", true: "explicitly-rejected"}[rejected], func(t *testing.T) {
			h, _, as, c := parkUpgradeFixture(t)
			if op := startTestUpgrade(h, "known-not-closed", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if rejected {
				upgradeTick(h, ServerObservation{})
				close := upgradeRead(h).ManagerParks[0].CloseRequestID
				if op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: close, Rejected: true, Error: "Rejected before executing native close"}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			batch := upgradeRead(h)
			if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); err != nil {
				t.Fatal("known unexecuted close left an unknown fence", err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				closing, err := managerRunClosing(t.Context(), tx.Q(), as.RunID)
				if err != nil || closing {
					t.Fatal("known unexecuted close falsely classified as unknown", closing, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
