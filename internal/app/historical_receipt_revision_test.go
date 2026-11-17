package app

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestLegacyReceiptRetainsAdmittedConversation(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	message, delivery := h.say("Original shared source admitted into the manager context")
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: "legacy-shared-thread", TurnID: "legacy-shared-turn", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_turn_contexts(request_id,conversation_id,context_revision) VALUES(?,?,1)`, turn.RequestID, as.Manager.ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !attachment.Reconciliation || attachment.RequestID != turn.RequestID {
		t.Fatal("original admitted attachment unavailable", attachment, err)
	}
	raw, err := h.app.RetainedNativeReceipt(t.Context(), as.Manager.ConversationID, turn.RequestID)
	if err != nil || len(raw) != 0 {
		t.Fatal("missing legacy original blocked live history", raw, err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.ObserveNativeTurnTerminal(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	original := json.RawMessage(`{"id":"legacy-shared-turn","status":"completed","items":[]}`)
	if err := h.app.RecordRolloutEvent(t.Context(), as.Manager.ConversationID, turn.ThreadID, turn.TurnID, "", "native_receipt", original); err != nil {
		t.Fatal(err)
	}
	if got, err := h.app.RetainedNativeReceipt(t.Context(), as.Manager.ConversationID, turn.RequestID); err != nil || !store.SemanticJSONEqual(string(got), string(original)) {
		t.Fatal("admitted original unavailable", string(got), err)
	}
	if _, err := h.app.RetainedNativeReceipt(t.Context(), "main", turn.RequestID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("shared source widened original receipt scope", err)
	}
}

func TestTaskUploadRecoveryPreservesAcceptanceProducer(t *testing.T) {
	h, _, accept, runtime := acceptanceEvidenceFixture(t)
	feedback, _ := h.uploadResultFile(accept, "selected-feedback.log", []byte("selected original feedback"))
	draft, _ := h.uploadResultFile(accept, "private-review-draft.log", []byte("unsubmitted private draft"))
	if _, err := h.app.ReadRunContextFile(t.Context(), runPrincipal(accept.RunID, accept.Role), draft.FileID); err != nil {
		t.Fatal("producer cannot read its own draft", err)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Files: []protocol.ArtifactFile{feedback}}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("missing actual implementation successor")
	}
	p := runPrincipal(next.RunID, next.Role)
	if _, err := h.app.DeliverableFile(t.Context(), p, feedback.FileID); err != nil {
		t.Fatal("frozen feedback unavailable", err)
	}
	if _, err := h.app.DeliverableFile(t.Context(), p, draft.FileID); err == nil {
		t.Fatal("same Task published an unsubmitted acceptance draft")
	}
	if _, err := h.app.ReadRunContextFile(t.Context(), p, draft.FileID); err == nil {
		t.Fatal("context read bypassed draft ownership")
	}
	context, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files"})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range context.Materials.Files {
		if file.FileID == draft.FileID {
			t.Fatal("raw upload index exposed private review draft")
		}
	}
	if op := h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result := testArtifact(h.task(next.TaskID).WorkspaceName)
	result.Files = []protocol.ArtifactFile{draft}
	if op := h.runAs(next, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("result laundered an acceptance draft", op)
	}
	// A stored historical invalid reference cannot publish the wrong producer.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		raw, _ := json.Marshal(result)
		return tx.InsertArtifact(t.Context(), h.ids.New(protocol.PrefixArtifact), next.TaskID, next.RunID, "result", string(raw), "fixture", h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if _, _, err := store.GetFinalizedDeliverableFile(t.Context(), tx.Q(), draft.FileID); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("invalid result published draft metadata", err)
		}
		if _, err := store.ReadFinalizedDeliverableFile(t.Context(), tx.Q(), draft.FileID, 0, 8, h.store.FileStore()); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("invalid result published draft bytes", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeManagerParkDistinguishesObservedTerminalFromLocalInterruption(t *testing.T) {
	h, next, turn, _, command := historicalParkFixture(t)
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if op := startPreparedTestUpgrade(h, "observed-terminal", command); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	if pending, err := h.app.NativeReceiptPending(t.Context(), turn.RequestID); err != nil || !pending {
		t.Fatal("local cleanup invented an observed native terminal")
	}
	turn.Status = store.TurnInterrupted
	for range 2 {
		if err := h.app.ObserveNativeTurnTerminal(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
	}
	upgradeTick(h, ServerObservation{})
	if len(upgradeRead(h).View.ManagerParks) != 0 {
		t.Fatal("known native terminal was blocked by incomplete presentation")
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), turn.RequestID); err != nil || !pending {
		t.Fatal("observing terminal consumed unresolved sources", pending, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE conversation_id=? AND thread_id=? AND turn_id=? AND kind='native_terminal'`, next.Manager.ConversationID, turn.ThreadID, turn.TurnID) != 1 || h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND turn_id=? AND kind='native_receipt'`, turn.ThreadID, turn.TurnID) != 0 {
		t.Fatal("terminal metadata duplicated full history")
	}
}
