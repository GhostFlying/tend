package orca

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func nativeFixture(t *testing.T) (*workerFixture, *worker, *Agent) {
	t.Helper()
	f, w, a := failedWorker(t)
	a.AgentType = "traex"
	a.ProviderSession.TranscriptPath = ""
	a.RootTurn = &RootTurn{Version: 1, PaneKey: a.PaneKey, LaunchID: "launch", SessionID: a.ProviderSession.ID, TurnID: "turn-root", ObservedAt: 300, StartedAt: 150, CompletedAt: 200, Source: "native-api", State: "failed", Operations: "settled", ErrorCode: "response_stream_disconnected", Retryable: true}
	previous := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		data, err := previous(ctx, dir, args)
		if err != nil {
			return data, err
		}
		var response map[string]any
		if json.Unmarshal(data, &response) != nil {
			return data, err
		}
		response["_meta"] = map[string]any{"runtimeId": "runtime"}
		data, err = json.Marshal(response)
		return data, err
	}
	return f, w, a
}

func TestFault_NativeRootRecoveryWithoutJSONL(t *testing.T) {
	f, w, a := nativeFixture(t)
	terminal, pane, session := w.st.Terminal, w.st.Pane, a.ProviderSession.ID
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if w.st.Recovery == nil || w.st.Recovery.Attempts != 1 || w.st.Recovery.Phase != recoveryRunning {
		t.Fatalf("%+v", w.st.Recovery)
	}
	if w.st.Terminal != terminal || w.st.Pane != pane || a.ProviderSession.ID != session || f.orca.count("terminal close", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("native connection recovery changed the original session")
	}
	var restored workerState
	if err := json.Unmarshal([]byte(f.thread), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Recovery.Attempts != 1 {
		t.Fatal("budget was not journaled")
	}
	// A second failed turn cannot reset the allowance after journal reconstruction.
	w.st = restored
	if err := w.recoverFailure(t.Context(), *a); err == nil {
		t.Fatal("second recovery permitted")
	}
}

func TestFault_NativeRootPendingOrUnknownDoesNotRecover(t *testing.T) {
	for _, operations := range []string{"pending", "unknown"} {
		t.Run(operations, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.RootTurn.Operations = operations
			if _, done, err := w.step(t.Context()); err != nil || done {
				t.Fatalf("done=%v err=%v", done, err)
			}
			if w.st.Recovery != nil || f.orca.count("terminal send", nil) != 0 {
				t.Fatal("unsettled operations recovered")
			}
		})
	}
}

func TestNativeRootFactsAreBoundToOwnerAndAllowlist(t *testing.T) {
	for _, change := range []func(*RootTurn){
		func(r *RootTurn) { r.SessionID = "child" }, func(r *RootTurn) { r.PaneKey = "other" },
		func(r *RootTurn) { r.StartedAt = 1 }, func(r *RootTurn) { r.ErrorCode = "other" },
		func(r *RootTurn) { r.Retryable = false }, func(r *RootTurn) { r.Operations = "unknown" },
	} {
		_, _, a := nativeFixture(t)
		change(a.RootTurn)
		if _, err := nativeFailedTurn(*a, 100); err == nil {
			t.Fatal("unsafe evidence accepted")
		}
	}
}

func TestFault_NativeLaunchReplacementHoldsWorker(t *testing.T) {
	_, w, a := nativeFixture(t)
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.RootTurn.LaunchID = "replacement"
	if _, _, err := w.agent(t.Context()); !errors.Is(err, harness.ErrHeld) {
		t.Fatalf("replacement: %v", err)
	}
}

func TestNativeCompletionKeepsHostSubsecondOrdering(t *testing.T) {
	_, w, a := nativeFixture(t)
	w.st.Turn = 200_500
	a.RootTurn.StartedAt, a.RootTurn.CompletedAt, a.RootTurn.HostStartedAt, a.RootTurn.ObservedAt = 200_000, 200_000, 200_600, 201_000
	a.RootTurn.State = "completed"
	observed, _, err := w.agent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !answered(observed, w.st.Turn) {
		t.Fatalf("subsecond completion rejected: %+v", observed)
	}
}

func TestNativeCapabilityFailureRetainsOperationFence(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.Operations = "pending"
	previous := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if args[0] == "status" {
			return nil, errors.New("runtime status unavailable")
		}
		return previous(ctx, dir, args)
	}
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if w.st.Recovery != nil || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("failed capability verification downgraded native fence")
	}
}

func TestNativeOtherKeepsOnlyTranscriptBackedContextRecovery(t *testing.T) {
	f, w, a := nativeFixture(t)
	session := recoveryTranscript(t, "")
	a.ProviderSession = &session
	a.RootTurn.ErrorCode, a.RootTurn.Retryable = "other", false
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if w.st.Recovery == nil || w.st.Recovery.Code != "invalid_encrypted_content" || f.orca.count("terminal close", nil) != 1 {
		t.Fatal("validated context recovery was lost")
	}
}

func TestNativeObservationResolutionSurvivesJournalRestart(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.State = "unknown"
	if err := w.recordRootObservation(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	var restored workerState
	if err := json.Unmarshal([]byte(f.thread), &restored); err != nil {
		t.Fatal(err)
	}
	w.st = restored
	a.RootTurn.State = "running"
	if err := w.recordRootObservation(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	if w.st.RootObservationPhase != "known" || w.st.RootObservationRevision != 2 {
		t.Fatalf("%+v", w.st)
	}
	if last := w.pending[len(w.pending)-1]; last.Kind != "worker.root_observation_resolved" {
		t.Fatalf("%+v", last)
	}
}

func TestNativeResumeWaitsForRootAndOperationSettlement(t *testing.T) {
	for _, tc := range []struct{ state, operations string }{{"running", "unknown"}, {"completed", "pending"}, {"completed", "unknown"}} {
		t.Run(tc.state+"-"+tc.operations, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.MainAgent.Outcome = "success"
			a.RootTurn.State, a.RootTurn.Operations = tc.state, tc.operations
			if err := w.begin(t.Context(), "codex"); err != nil {
				t.Fatal(err)
			}
			if f.orca.count("terminal send", nil) != 0 {
				t.Fatal("resuming bypassed native settlement fence")
			}
		})
	}
}

func TestNativeUnknownObservationReconcilesOnlySavedRequest(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.State, a.RootTurn.Operations = "unknown", "unknown"
	a.MainAgent.Outcome = "success"
	w.st.Send, w.st.SendText = "original-request", "original prompt"
	if err := w.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if n := f.orca.count("terminal send", func(args []string) bool { return flag(args, "--retry-request") == "original-request" }); n != 1 {
		t.Fatalf("original request reconciled %d times", n)
	}
	if w.st.Send != "" {
		t.Fatal("saved receipt not settled")
	}
}

func TestNativeCompletedRootWithUnknownOperationsRequestsReconciliation(t *testing.T) {
	_, w, a := nativeFixture(t)
	a.MainAgent.Outcome = "success"
	a.RootTurn.State, a.RootTurn.Operations = "completed", "unknown"
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	for _, event := range w.pending {
		if event.Kind == "worker.root_observation_unknown" {
			return
		}
	}
	t.Fatal("unknown operation results did not request reconciliation")
}

func TestNativeUnknownHistorySettlesSavedRecoveryReceipt(t *testing.T) {
	f, w, a := nativeFixture(t)
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, Code: "response_stream_disconnected", SessionID: a.ProviderSession.ID, TurnID: a.RootTurn.TurnID, TurnAt: 200, Terminal: w.st.Terminal, Pane: w.st.Pane, LaunchID: "launch", RuntimeID: "runtime", OriginalPrompt: w.st.Said, ContinuationIssued: true}
	w.st.Send, w.st.SendText = "recovery-request", w.recoveryBrief()
	a.RootTurn.State, a.RootTurn.Operations = "unknown", "unknown"
	if err := w.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if w.st.Recovery.Phase != recoveryRunning || w.st.Send != "" {
		t.Fatalf("receipt not reconciled: %+v", w.st)
	}
	if n := f.orca.count("terminal send", func(args []string) bool { return flag(args, "--retry-request") == "recovery-request" }); n != 1 {
		t.Fatalf("recovery request settled %d times", n)
	}
	if f.orca.count("terminal close", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("receipt lookup rebuilt the session")
	}
}

func TestNativeEvidenceRequirementSurvivesObservationOmission(t *testing.T) {
	_, w, a := nativeFixture(t)
	a.RootTurn.Operations = "unknown"
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.RootTurn = nil
	a.MainAgent.Outcome = "success"
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("missing evidence completed: done=%v err=%v", done, err)
	}
	if !w.st.RootObservationRequired {
		t.Fatal("native fence not journaled")
	}
}

func TestNativeInterruptedContinuationWaitsForOperations(t *testing.T) {
	for _, operations := range []string{"pending", "unknown"} {
		t.Run(operations, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			w.st.Interrupted = true
			a.MainAgent.Outcome = "cancellation"
			a.RootTurn.State, a.RootTurn.Operations = "cancelled", operations
			if _, done, err := w.step(t.Context()); err != nil || done {
				t.Fatalf("done=%v err=%v", done, err)
			}
			if f.orca.count("terminal send", nil) != 0 {
				t.Fatal("interrupted continuation bypassed operation fence")
			}
		})
	}
}

func TestNativeHandbackPersistsPendingIdentityRebind(t *testing.T) {
	_, w, a := nativeFixture(t)
	old := *a.RootTurn
	w.st.RootObservationRequired = true
	w.st.RootLaunchID, w.st.RootRuntimeID = "old-launch", "old-runtime"
	w.spec.HandBack = true
	a.RootTurn = nil
	if err := w.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if !w.st.RootRebindPending {
		t.Fatal("missing replacement observation lost handback authorization")
	}
	a.RootTurn = &old
	a.RootTurn.LaunchID = "new-launch"
	a.RootTurn.HostStartedAt, a.RootTurn.ObservedAt = w.st.Turn+1, w.st.Turn+2
	a.RootTurn.TurnID = "handback-turn"
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.st.RootRebindPending || w.st.RootLaunchID != "new-launch" {
		t.Fatalf("replacement not adopted: %+v", w.st)
	}
}

func TestNativeSendWaitsForNewTurnEvidenceBeforeBindingItsEpoch(t *testing.T) {
	_, w, a := nativeFixture(t)
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.Turn = 400
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.RootTurn.TurnID = "next-turn"
	a.RootTurn.HostStartedAt, a.RootTurn.ObservedAt = 401, 402
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.st.RootObservedTurnID != "next-turn" || w.st.RootObservedForTurn != 400 {
		t.Fatalf("new turn incorrectly fenced: %+v", w.st)
	}
}

func TestNativeCapabilityBlocksCompletionBeforeFirstObservation(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.AgentType = "traex"
	a.RootTurn = nil
	a.MainAgent.Outcome = "success"
	f.ws.opts.Now = func() time.Time { return time.UnixMilli(1000) }
	w.st.Turn = 1000
	a.StateStartedAt, a.MainAgent.StateStartedAt = 1001, 1001
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("completed without native observation: %v %v", done, err)
	}
	if !w.st.RootObservationRequired {
		t.Fatal("capability was not persisted")
	}
}

func TestVerifiedLegacyRuntimeCanRecoverFromCapabilityReadFailure(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.AgentType = "traex"
	a.RootTurn = nil
	a.MainAgent.Outcome = "success"
	previous := f.ws.opts.Client.Exec
	fail := true
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if args[0] == "status" {
			if fail {
				return nil, errors.New("transient status read failure")
			}
			return []byte(`{"ok":true,"result":{"runtime":{"runtimeId":"runtime","capabilities":[]}}}`), nil
		}
		return previous(ctx, dir, args)
	}
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("unverified legacy completed: %v %v", done, err)
	}
	if w.st.RootObservationRequired {
		t.Fatal("unverified absence falsely established native support")
	}
	fail = false
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("verified legacy did not resume: %v %v", done, err)
	}
}

func TestNativeSettlementSupersedesOnlyOlderAggregateEvidence(t *testing.T) {
	_, _, a := nativeFixture(t)
	a.State = stateWorking
	a.UpdatedAt = 150
	if !backgroundSettled(*a, Wait{}) {
		t.Fatal("native convergence still depends on stale JSONL aggregate")
	}
	a.UpdatedAt = 350
	if backgroundSettled(*a, Wait{}) {
		t.Fatal("older native read ignored newer background activity")
	}
}

func TestFault_UnknownRecoverySendRetainsWorkerAndReconcilesOriginalReceipt(t *testing.T) {
	f, w, _ := nativeFixture(t)
	f.orca.loseSend = sendAttempts
	_, done, err := w.step(t.Context())
	if done || !errors.Is(err, errRecoveryUncertain) {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if w.st.Send != "req-1" || w.st.Recovery.Attempts != 1 {
		t.Fatalf("state=%+v", w.st)
	}
	var persisted workerState
	if err := json.Unmarshal([]byte(f.thread), &persisted); err != nil {
		t.Fatal(err)
	}
	w.events, w.done = make(chan harness.WorkerEvent), make(chan struct{})
	w.reconciling = true
	f.orca.loseSend = 100000
	w.spec.OnTakeover = func(string) error { t.Error("unknown send reported as human takeover"); return nil }
	ctx, cancel := context.WithCancel(t.Context())
	if err := w.openBridge(ctx); err != nil {
		t.Fatal(err)
	}
	go w.run(ctx)
	received := false
	for !received {
		select {
		case ev := <-w.events:
			received = ev.Kind == "worker.root_observation_unknown"
		case <-w.done:
			t.Fatal("unknown send released worker")
		case <-time.After(3 * time.Second):
			t.Fatal("missing durable uncertainty fact")
		}
	}
	select {
	case <-w.done:
		t.Fatal("worker finished without reconciliation")
	default:
	}
	cancel()
	<-w.done
	if f.orca.count("terminal close", nil) != 0 || f.orca.count("terminal send", func(args []string) bool { return flag(args, "--text") == esc }) != 0 {
		t.Fatal("uncertain session mutated on shutdown")
	}
	f.orca.loseSend = 0
	resumed := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(w.spec.Role), st: persisted}
	if err := resumed.begin(t.Context(), "traex"); err != nil {
		t.Fatal(err)
	}
	if resumed.st.Recovery.Attempts != 1 || resumed.st.Send != "" || resumed.st.Recovery.Phase != recoveryRunning {
		t.Fatalf("state=%+v", resumed.st)
	}
	f.orca.mu.Lock()
	defer f.orca.mu.Unlock()
	sends := 0
	for _, args := range f.orca.calls {
		if len(args) > 1 && args[0] == "terminal" && args[1] == "send" {
			sends++
			if sends > 1 && flag(args, "--retry-request") != "req-1" {
				t.Fatal("fresh request issued for unknown send")
			}
		}
	}
	if sends < sendAttempts+1 {
		t.Fatalf("sends=%d", sends)
	}
}

func TestFault_RecoverySubmissionWithoutReceiptStaysUncertainAfterRestart(t *testing.T) {
	f, w, a := nativeFixture(t)
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, SessionID: a.ProviderSession.ID, TurnAt: a.MainAgent.StateStartedAt, OriginalPrompt: w.st.Said, ContinuationIssued: true}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var st workerState
		if err := json.Unmarshal([]byte(f.thread), &st); err != nil {
			t.Fatal(err)
		}
		resumed := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(w.spec.Role), st: st}
		if err := resumed.begin(t.Context(), "traex"); !errors.Is(err, errRecoveryUncertain) {
			t.Fatalf("err=%v", err)
		}
		if resumed.st.Recovery.Attempts != 1 {
			t.Fatal("budget changed")
		}
	}
	if f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("unconfirmed submission replayed")
	}
}

func TestFailedRootPendingInventoryReplacesUnknown(t *testing.T) {
	_, w, a := nativeFixture(t)
	a.RootTurn.Operations = "unknown"
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := len(w.pending)
	a.RootTurn.Operations = "pending"
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.pending) == before || w.st.RootObservationPhase != "pending" {
		t.Fatal("known pending inventory remained unknown")
	}
}
func TestUnknownRecoveryReceiptReconcilesWithoutRestart(t *testing.T) {
	f, w, _ := nativeFixture(t)
	f.orca.loseSend = sendAttempts
	if _, _, err := w.step(t.Context()); !errors.Is(err, errRecoveryUncertain) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if !w.awaitRecoveryReconciliation(ctx) || w.st.Send != "" || w.st.Recovery.Phase != recoveryRunning || w.st.Recovery.Attempts != 1 {
		t.Fatalf("state=%+v", w.st)
	}
}
func TestMissingRecoveryObservationIsNotOwnershipChange(t *testing.T) {
	f, w, a := nativeFixture(t)
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, SessionID: a.ProviderSession.ID, TurnID: a.RootTurn.TurnID, TurnAt: 200, LaunchID: "launch", RuntimeID: "runtime", OriginalPrompt: w.st.Said, ContinuationIssued: true}
	w.st.Send, w.st.SendText = "original-receipt", w.recoveryBrief()
	a.RootTurn = nil
	err := w.begin(t.Context(), "traex")
	if !errors.Is(err, errRecoveryUncertain) || errors.Is(err, harness.ErrHeld) || f.orca.count("terminal send", nil) != 0 {
		t.Fatalf("err=%v", err)
	}
}

func TestFault_HandbackRecoveryCanReconcileObservedSubmission(t *testing.T) {
	_, w, a := nativeFixture(t)
	w.spec.HandBack = true
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, SessionID: a.ProviderSession.ID, TurnID: a.RootTurn.TurnID, TurnAt: 200, OriginalPrompt: w.st.Said, ContinuationIssued: true}
	if err := w.begin(t.Context(), "traex"); !errors.Is(err, errRecoveryUncertain) {
		t.Fatalf("begin: %v", err)
	}
	// The original send is subsequently proven by the observed native prompt.
	a.Prompt = w.recoveryBrief()
	a.StateStartedAt = w.st.Turn + 1
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if !w.awaitRecoveryReconciliation(ctx) {
		t.Fatal("matching original recovery prompt never reconciles during handback")
	}
}

func TestFault_UnknownNativeTurnIsNotTakeoverDuringRecovery(t *testing.T) {
	_, w, a := nativeFixture(t)
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, SessionID: a.ProviderSession.ID, TurnID: a.RootTurn.TurnID, TurnAt: 200, LaunchID: "launch", RuntimeID: "runtime", OriginalPrompt: w.st.Said}
	// The same native launch/session exists, but the history read is temporarily unavailable.
	a.RootTurn.State, a.RootTurn.Operations = "unknown", "unknown"
	a.MainAgent = nil
	a.State = stateWorking
	err := w.begin(t.Context(), "traex")
	if !errors.Is(err, errRecoveryUncertain) || errors.Is(err, harness.ErrHeld) {
		t.Fatalf("unknown native evidence should reconcile, got %v", err)
	}
}

func TestFault_SettledNativeCompletionOverridesOlderAggregate(t *testing.T) {
	_, w, a := nativeFixture(t)
	a.State = stateWorking
	a.MainAgent = nil
	a.UpdatedAt = 150
	a.RootTurn.State = "completed"
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("native completed+settled is newer than aggregate, done=%v err=%v", done, err)
	}
}

func TestFault_TransientCapabilityFailureDuringNativeRecoveryDoesNotHold(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.MainAgent = nil
	a.State = stateWorking
	a.UpdatedAt = 150
	observed, _, err := w.agent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	previous := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if args[0] == "status" {
			return nil, errors.New("temporary status failure")
		}
		return previous(ctx, dir, args)
	}
	err = w.recoverFailure(t.Context(), observed)
	if !errors.Is(err, errRecoveryUncertain) || errors.Is(err, harness.ErrHeld) {
		t.Fatalf("single transient capability read failed recovery as %v", err)
	}
}

func TestFault_NativeFailureWithNonzeroTurnDurationCanRecover(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.CompletedAt = 250 // StartedAt is 200; normal non-instantaneous failure.
	_, done, err := w.step(t.Context())
	if err != nil || done {
		t.Fatalf("native recovery failed to start, done=%v err=%v; recovery=%+v", done, err, w.st.Recovery)
	}
	if w.st.Recovery == nil || w.st.Recovery.Phase != recoveryRunning || f.orca.count("terminal send", nil) != 1 {
		t.Fatalf("native recovery did not continue once: %+v", w.st.Recovery)
	}
}
