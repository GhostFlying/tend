package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_ManagerProjectionRecoversOriginalTail(t *testing.T) {
	h, runtime, issue, as, _ := reviewManagerAssignment(t)
	ctx := t.Context()
	selection, named, _, err := h.app.HostedRuntimeStartupSelection(ctx, as.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	thread, root := "native-thread", "root-turn"
	source, request := h.ids.New(protocol.PrefixMessage), h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: source, ConversationID: as.Manager.ConversationID, Author: "system", Body: "Original source", CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: request, ConversationID: as.Manager.ConversationID, IssueID: issue.IssueID, AssignmentEpoch: issue.AssignmentEpoch, Selection: selection, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: request, ThreadID: thread, TurnID: root, MessageID: source, Status: store.TurnInterrupted}, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}

	commandRaw := json.RawMessage(`{"id":"cmd","type":"commandExecution","command":"existing command","status":"failed"}`)
	facts := []string{
		`{"thread_id":"native-thread","root_turn_id":"root-turn","native":{"Kind":"item_completed","TurnID":"continued","ItemID":"cmd","ItemType":"commandExecution","Tool":{"Name":"shell","Input":"existing command","Output":"original failure","Status":"failed"},"Raw":` + string(commandRaw) + `}}`,
		`{"thread_id":"native-thread","root_turn_id":"root-turn","native":{"Kind":"item_completed","TurnID":"continued","ItemID":"reason","ItemType":"reasoning","Raw":{"id":"reason","type":"reasoning","summary":["retained"]}}}`,
		`{"thread_id":"native-thread","root_turn_id":"root-turn","native":{"Kind":"item_completed","TurnID":"continued","ItemID":"dynamic","ItemType":"dynamicToolCall","Tool":{"Name":"original_tool","Output":"original result","Status":"completed"},"Raw":{"id":"dynamic","type":"dynamicToolCall","status":"completed"}}}`,
		`{"thread_id":"","disconnected":true}`,
	}
	for i, body := range facts {
		p := protocol.ManagerNativeEvent{Sequence: int64(i + 1), Body: json.RawMessage(body)}
		if op := h.runAs(as, protocol.OpManagerNativeEvent, p, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if err := h.app.RecordRolloutEvent(ctx, as.Manager.ConversationID, thread, "continued", "cmd", "item_completed", commandRaw); err != nil {
		t.Fatal(err)
	}
	last := int64(len(facts))
	assertManagerProjectionWatermarks(t, h, as.RunID, last, 0)
	if err := h.app.projectManagerNativeEvent(ctx, as.RunID, 1); err != nil {
		t.Fatal(err)
	}
	assertManagerProjectionWatermarks(t, h, as.RunID, last, 0)
	originalClose, err := h.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claimed := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var closeCommand protocol.ManagerNativeRequest
	if json.Unmarshal(claimed.Result, &closeCommand) != nil || closeCommand.ID != originalClose {
		t.Fatal("original close was not claimed", claimed)
	}
	closeReply := protocol.ManagerNativeReply{ID: originalClose, LastSequence: last}
	early, earlyErr := h.app.Execute(ctx, runPrincipal(as.RunID, as.Role), h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), closeReply, nil), ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if protocol.CodeOf(earlyErr) != protocol.CodeUnknownOutcome && (early.Error == nil || early.Error.Code != protocol.CodeUnknownOutcome) {
		t.Fatal("close bypassed unapplied original events", early, earlyErr)
	}
	stop := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process failed", LastSequence: last}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if stop.Status != protocol.OpSucceeded {
		t.Fatal(stop)
	}
	if got := h.run(as.RunID).ObservedState; got != protocol.RunFailed {
		t.Fatalf("original failure disappeared: %v", got)
	}

	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TRIGGER test_projection_failure BEFORE UPDATE OF applied ON manager_native_events
 WHEN NEW.applied=1 AND OLD.seq=1 BEGIN SELECT RAISE(ABORT,'injected after projection'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.projectManagerNativeEvent(ctx, as.RunID, 1); err == nil || !strings.Contains(err.Error(), "injected after projection") {
		t.Fatal("SQLite fault did not reach the original application transaction", err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='cmd'`) != 0 ||
		h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE item_id='cmd'`) != 1 {
		t.Fatal("failed application leaked a tool projection or modified the legacy material")
	}
	assertManagerProjectionWatermarks(t, h, as.RunID, last, 0)
	if err := h.app.projectManagerNativeEvent(ctx, as.RunID, 2); err != nil {
		t.Fatal(err)
	}
	assertManagerProjectionWatermarks(t, h, as.RunID, last, 0)
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `DROP TRIGGER test_projection_failure`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The projected row and application marker can both reach the writer
	// before SQLite rejects the commit. The caller still reads the original
	// application row before deciding anything remains missing.
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `CREATE TABLE test_projection_commit_guard
 (run_id TEXT REFERENCES runs(run_id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `CREATE TRIGGER test_projection_commit_failure AFTER UPDATE OF applied ON manager_native_events
 WHEN NEW.applied=1 AND OLD.seq=1 BEGIN
  INSERT INTO test_projection_commit_guard(run_id) VALUES('nonexistent-original-run');
 END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.projectManagerNativeEvent(ctx, as.RunID, 1); err == nil || !strings.Contains(err.Error(), "store: commit:") {
		t.Fatal("fault did not reach the SQLite commit boundary", err)
	}
	assertManagerProjectionWatermarks(t, h, as.RunID, last, 0)
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='cmd'`) != 0 {
		t.Fatal("the failed commit made a tool projection visible")
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `DROP TRIGGER test_projection_commit_failure`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Cancellation cannot authorize another write. Reopen SQLite and start
	// recovery with no attachment, RPC consumer or in-memory cursor.
	cancelled, cancelAttempt := context.WithCancel(ctx)
	cancelAttempt()
	if err := h.app.projectManagerNativeEvent(cancelled, as.RunID, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled application acquired a new write authority", err)
	}
	assertManagerProjectionWatermarks(t, h, as.RunID, last, 0)
	var databasePath string
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		var index int
		var name string
		return tx.Q().QueryRowContext(ctx, `PRAGMA database_list`).Scan(&index, &name, &databasePath)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, databasePath, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	h.store = reopened
	h.app = New(reopened, h.ids, h.clock.Now)
	recoverCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.app.RunManagerEventRecovery(recoverCtx, slog.New(slog.DiscardHandler))
	}()
	defer func() { cancel(); <-done }()
	waitCtx, stopWaiting := context.WithTimeout(ctx, 10*time.Second)
	defer stopWaiting()
	for {
		wake := h.app.Changes().Wait()
		cursor, err := h.app.ManagerAppliedCursor(waitCtx, as.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if cursor == last {
			break
		}
		select {
		case <-wake:
		case <-waitCtx.Done():
			t.Fatal("the original tail was not recovered after restart", cursor)
		}
	}
	for item, status := range map[string]string{"cmd": "failed", "dynamic": "completed"} {
		if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses u JOIN conversation_messages m USING(message_id)
 WHERE u.thread_id=? AND u.turn_id='continued' AND u.item_id=? AND u.status=? AND m.reply_to=?`, thread, item, status, source) != 1 {
			t.Fatalf("original item %s lost its original status or source", item)
		}
	}
	if h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND kind='item_completed'`, thread) != 3 {
		t.Fatal("original item stages were lost or duplicated")
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND request_id=? AND status='unknown'`, as.RunID, originalClose) != 1 {
		t.Fatal("database application did not preserve the original uncertain close")
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=?`, as.RunID) != 1 {
		t.Fatal("database application issued a native command")
	}
	runtime.Sequence++
	premature := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var beforeClose claimResult
	if err := json.Unmarshal(premature.Result, &beforeClose); err != nil || beforeClose.Assignment.RunID != "" {
		t.Fatal("a successor claimed before the original close settled", premature, err)
	}
	close := h.runAs(as, protocol.OpManagerNativeReply, closeReply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if close.Status != protocol.OpSucceeded {
		t.Fatal("original close did not settle after recovery", close)
	}
	runtime.Sequence++
	successor := reviewClaimManager(t, h, runtime)
	if successor.TaskID != as.TaskID || successor.RunID == as.RunID || successor.Generation <= as.Generation {
		t.Fatal("actual runner did not claim a successor on the original task", successor)
	}
	if got := h.run(as.RunID).ObservedState; got != protocol.RunFailed {
		t.Fatalf("projection changed original outcome: %v", got)
	}
}

func assertManagerProjectionWatermarks(t *testing.T, h *harness, run string, received, applied int64) {
	t.Helper()
	last, err := h.app.LastManagerNativeEvent(t.Context(), run)
	if err != nil || last != received {
		t.Fatalf("received cursor = %d, want %d (%v)", last, received, err)
	}
	last, err = h.app.ManagerAppliedCursor(t.Context(), run)
	if err != nil || last != applied {
		t.Fatalf("applied cursor = %d, want %d (%v)", last, applied, err)
	}
}

func TestFault_ManagerAppliedCursorStopsAtFirstMissingSequence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present []int64
		pending int64
		want    int64
	}{
		{name: "empty"},
		{name: "multiple leading gaps", present: []int64{3}},
		{name: "multiple interior gaps", present: []int64{1, 4, 5}, want: 1},
		{name: "earlier unapplied event", present: []int64{1, 2, 3, 6}, pending: 2, want: 1},
		{name: "first gap before unapplied event", present: []int64{1, 4}, pending: 4, want: 1},
		{name: "continuous", present: []int64{1, 2, 3}, want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				for _, seq := range tc.present {
					if _, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json,applied) VALUES(?,?,'{}',?)`, as.RunID, seq, seq != tc.pending); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var received int64
			if len(tc.present) != 0 {
				received = tc.present[len(tc.present)-1]
			}
			assertManagerProjectionWatermarks(t, h, as.RunID, received, tc.want)
		})
	}
}

func TestFault_ManagerProjectionContinuesAfterOriginalUnclaimedCallbackRejection(t *testing.T) {
	for _, tc := range []struct {
		name           string
		legacyRetained bool
	}{
		{name: "durable after receipt consumption"},
		{name: "legacy stop with original receipt", legacyRetained: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			ctx := t.Context()
			original, err := h.app.QueueManagerNative(ctx, as.RunID, "tool_result", json.RawMessage(`{"thread_id":"native","turn_id":"original-turn","call_id":"original-call","tool_result":{"Success":false,"Text":"original failure"}}`))
			if err != nil {
				t.Fatal(err)
			}
			facts := []string{
				`{"thread_id":"native","call":{"ThreadID":"native","TurnID":"original-turn","CallID":"original-call","Tool":"existing_tool"}}`,
				`{"thread_id":"native","native":{"Kind":"item_completed","TurnID":"original-turn","ItemID":"later","ItemType":"reasoning","Raw":{"id":"later"}}}`,
			}
			for i, raw := range facts {
				p := protocol.ManagerNativeEvent{Sequence: int64(i + 1), Body: json.RawMessage(raw)}
				if op := h.runAs(as, protocol.OpManagerNativeEvent, p, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process stopped", LastSequence: 2}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			assertManagerProjectionWatermarks(t, h, as.RunID, 2, 0)
			if tc.legacyRetained {
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					// Older writers left the durable result in produced state
					// even though the original request was definitively rejected.
					_, err := tx.Exec(ctx, `UPDATE conversation_tool_uses SET native_delivery_status='produced',native_delivery_error=''
 WHERE native_run_id=? AND native_delivery_id=?`, as.RunID, original)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				reply, ready, err := h.app.ManagerNativeResult(ctx, as.RunID, original)
				if err != nil || !ready || !reply.Rejected || reply.ID != original {
					t.Fatal("missing original rejection", ready, reply, err)
				}
				if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=?`, original) != 0 {
					t.Fatal("fixture did not retire the consumed receipt")
				}
			}
			for seq := int64(1); seq <= 2; seq++ {
				if err := h.app.projectManagerNativeEvent(ctx, as.RunID, seq); err != nil {
					t.Fatal(err)
				}
			}
			assertManagerProjectionWatermarks(t, h, as.RunID, 2, 2)
			if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE native_run_id=? AND native_delivery_id=?
 AND native_delivery_status='rejected' AND native_delivery_error<>'' AND native_result_success=0 AND native_result_output='original failure'`, as.RunID, original) != 1 {
				t.Fatal("recovery changed the original tool result or lost its delivery identity")
			}
			if h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE item_id='later'`) != 1 {
				t.Fatal("the original unapplied tail did not advance")
			}
		})
	}
}

func TestFault_ManagerProjectionCannotCrossGapOrUnknownCallback(t *testing.T) {
	for _, fault := range []string{"gap", "unknown"} {
		t.Run(fault, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			ctx := t.Context()
			var original string
			first := `{"thread_id":"native","native":{"Kind":"item_started","TurnID":"turn","ItemID":"first","Raw":{"id":"first"}}}`
			if fault == "unknown" {
				first = `{"thread_id":"native","call":{"ThreadID":"native","TurnID":"turn","CallID":"pending","Tool":"existing_tool"}}`
				var err error
				original, err = h.app.QueueManagerNative(ctx, as.RunID, "tool_result",
					json.RawMessage(`{"thread_id":"native","turn_id":"turn","call_id":"pending","tool_result":{"Success":false,"Text":"original failure"}}`))
				if err != nil {
					t.Fatal(err)
				}
				if op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			for i, body := range []string{first, `{"thread_id":"native","native":{"Kind":"item_completed","TurnID":"turn","ItemID":"later","Raw":{"id":"later"}}}`} {
				p := protocol.ManagerNativeEvent{Sequence: int64(i + 1), Body: json.RawMessage(body)}
				if op := h.runAs(as, protocol.OpManagerNativeEvent, p, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original physical stop", LastSequence: 2}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			head := managerProjectionHead{run: as.RunID, sequence: 1}
			if fault == "gap" {
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					_, err := tx.Exec(ctx, `DELETE FROM manager_native_events WHERE run_id=? AND seq=1`, as.RunID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				head.sequence = 2
			}
			for range 4 {
				err := h.app.projectManagerNativeEvent(ctx, head.run, head.sequence)
				if err == nil || fault == "gap" && !strings.Contains(err.Error(), "sequence has a gap") ||
					fault == "unknown" && !strings.Contains(err.Error(), "original receipt") {
					t.Fatal("original head failed for the wrong reason", err)
				}
				if err := h.app.recordManagerProjectionFailure(ctx, head, err); err != nil {
					t.Fatal(err)
				}
				h.clock.Advance(time.Second)
			}
			h.app = New(h.store, h.ids, h.clock.Now)
			if heads, _, err := h.app.managerProjectionHeads(ctx); err != nil || len(heads) != 0 {
				t.Fatal("persisted recovery limit was lost", heads, err)
			}
			assertManagerProjectionWatermarks(t, h, as.RunID, 2, 0)
			if h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE item_id='later'`) != 0 {
				t.Fatal("projection skipped the failed sequence")
			}
			if original != "" && h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown'`, original) != 1 {
				t.Fatal("recovery resolved a result without its original receipt")
			}
			if original != "" && h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=?`, as.RunID) != 1 {
				t.Fatal("recovery reissued an uncertain request")
			}
			if original != "" {
				late := protocol.ManagerNativeReply{ID: original, Method: "tool_result"}
				op := h.runAs(as, protocol.OpManagerNativeReply, late, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
				if op.Status != protocol.OpSucceeded {
					t.Fatal("original late callback receipt was rejected", op)
				}
				heads, _, err := h.app.managerProjectionHeads(ctx)
				if err != nil || len(heads) != 1 || heads[0] != head {
					t.Fatal("original receipt did not rearm the blocked head", heads, err)
				}
				for seq := int64(1); seq <= 2; seq++ {
					if err := h.app.projectManagerNativeEvent(ctx, as.RunID, seq); err != nil {
						t.Fatal(err)
					}
				}
				assertManagerProjectionWatermarks(t, h, as.RunID, 2, 2)
				if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE native_run_id=? AND native_delivery_id=?
 AND native_delivery_status='delivered' AND native_result_success=0`, as.RunID, original) != 1 {
					t.Fatal("confirmed delivery changed the tool's original failure")
				}
			}
		})
	}
}

func TestFault_ManagerProjectionInterruptedToolFromDirectIssueMessage(t *testing.T) {
	for _, scenario := range []string{"stop-before-projection", "stopped-after-projection", "close-reply-after-projection", "interrupted-before-close"} {
		t.Run(scenario, func(t *testing.T) {
			testManagerProjectionInterruptedTool(t, scenario)
		})
	}
}

func testManagerProjectionInterruptedTool(t *testing.T, scenario string) {
	t.Helper()
	h, _, issue, as, _ := reviewManagerAssignment(t)
	ctx := t.Context()
	id := as.Manager.ConversationID
	selection, named, _, err := h.app.HostedRuntimeStartupSelection(ctx, id)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	source, request := h.ids.New(protocol.PrefixMessage), h.ids.New(protocol.PrefixRequest)
	const thread, turn = "native-thread", "direct-owner-turn"
	now := h.clock.Now().UnixMilli()
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: source, ConversationID: id, Author: "owner", Body: "Original direct input", CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: request, ConversationID: id, IssueID: issue.IssueID, AssignmentEpoch: issue.AssignmentEpoch, Selection: selection, CreatedAt: now}); err != nil {
			return err
		}
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: request, ThreadID: thread, TurnID: turn, MessageID: source, Status: store.TurnInterrupted}, now)
	}); err != nil {
		t.Fatal(err)
	}
	putUnrelated := func() {
		t.Helper()
		if err := h.store.Write(ctx, func(tx *store.Tx) error {
			unrelated := h.ids.New(protocol.PrefixMessage)
			if _, err := tx.InsertMessage(ctx, store.MessageRow{Kind: store.KindToolUse, MessageID: unrelated, ConversationID: id, Author: "main_agent", ReplyTo: source, CreatedAt: now}); err != nil {
				return err
			}
			return tx.PutToolUse(ctx, store.ToolUseRow{MessageID: unrelated, ThreadID: thread, TurnID: turn, ItemID: "unrelated", Tool: "shell", Status: string(protocol.ToolRunning)})
		}); err != nil {
			t.Fatal(err)
		}
	}
	attachmentInterrupted := scenario == "interrupted-before-close"
	if !attachmentInterrupted {
		putUnrelated()
	}
	facts := []string{
		`{"thread_id":"native-thread","native":{"Kind":"item_started","TurnID":"direct-owner-turn","ItemID":"unfinished","ItemType":"commandExecution","Tool":{"Name":"shell","Input":"retained command","Status":"running"},"Raw":{"status":"inProgress"}}}`,
		`{"thread_id":"native-thread","native":{"Kind":"item_started","TurnID":"direct-owner-turn","ItemID":"background","ItemType":"commandExecution","Tool":{"Name":"shell","Status":"running"},"Raw":{"status":"inProgress"}}}`,
		`{"thread_id":"native-thread","native":{"Kind":"item_completed","TurnID":"direct-owner-turn","ItemID":"background","ItemType":"commandExecution","Tool":{"Name":"shell","Output":"original aggregated output","Status":"running"},"Raw":{"status":"backgrounded","aggregatedOutput":"original aggregated output"}}}`,
		`{"thread_id":"native-thread","native":{"Kind":"item_completed","TurnID":"direct-owner-turn","ItemID":"background-observation","ItemType":"commandExecution","Tool":{"Name":"shell","Output":"output from retained observation","Status":"running"},"Raw":{"status":"backgrounded","aggregatedOutput":"output from retained observation"}}}`,
		`{"thread_id":"native-thread","native":{"Kind":"item_started","TurnID":"direct-owner-turn","ItemID":"with-result","ItemType":"commandExecution","Tool":{"Name":"shell","Status":"running"}}}`,
		`{"thread_id":"native-thread","native":{"Kind":"item_completed","TurnID":"direct-owner-turn","ItemID":"with-result","ItemType":"commandExecution","Tool":{"Name":"shell","Output":"original failure","Status":"failed"}}}`,
	}
	for i, raw := range facts {
		p := protocol.ManagerNativeEvent{Sequence: int64(i + 1), Body: json.RawMessage(raw)}
		if op := h.runAs(as, protocol.OpManagerNativeEvent, p, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	last := int64(len(facts))
	stop := func() {
		if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process stopped", LastSequence: last}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	var closeID string
	if scenario == "stop-before-projection" {
		stop()
	} else {
		closeID, err = h.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		claimed := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
		var req protocol.ManagerNativeRequest
		if json.Unmarshal(claimed.Result, &req) != nil || req.ID != closeID {
			t.Fatal("original close was not claimed", claimed)
		}
	}
	for seq := int64(1); seq <= last; seq++ {
		if err := h.app.projectManagerNativeEvent(ctx, as.RunID, seq); err != nil {
			t.Fatal(err)
		}
		if attachmentInterrupted && seq == 2 {
			if err := h.app.SettleOpenTurns(ctx, thread); err != nil {
				t.Fatal(err)
			}
			if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='background' AND status='interrupted' AND output=''`) != 1 ||
				h.countWhere(`SELECT COUNT(*) FROM runs WHERE run_id=? AND native_closed_at=0`, as.RunID) != 1 ||
				h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='close' AND status='started'`, as.RunID) != 1 {
				t.Fatal("the attachment must have interrupted the tool before the original close settled")
			}
			putUnrelated()
		}
	}
	assertManagerProjectionWatermarks(t, h, as.RunID, last, last)
	if scenario != "stop-before-projection" {
		if !attachmentInterrupted && h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='unfinished' AND status='running'`) != 1 {
			t.Fatal("the restored start should still be running while the original close is unresolved")
		}
		backgroundStatus := string(protocol.ToolRunning)
		if attachmentInterrupted {
			backgroundStatus = string(protocol.ToolInterrupted)
		}
		if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='background' AND status=? AND output='original aggregated output'`, backgroundStatus) != 1 ||
			h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='background-observation' AND status='running' AND output<>''`) != 1 {
			t.Fatal("backgrounded observations should retain their output and interruption state while the original close is unresolved")
		}
		if scenario == "close-reply-after-projection" || attachmentInterrupted {
			if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID, LastSequence: last}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		} else {
			stop()
		}
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses u JOIN conversation_messages m USING(message_id)
 WHERE u.thread_id=? AND u.turn_id=? AND m.reply_to=? AND u.item_id='unfinished' AND u.input='retained command' AND u.status='interrupted'`, thread, turn, source) != 1 {
		t.Fatal("original tool without a result did not retain its source and interrupted state")
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses u JOIN conversation_messages m USING(message_id)
 WHERE u.thread_id=? AND u.turn_id=? AND m.reply_to=? AND u.status='interrupted' AND (
  (u.item_id='background' AND u.output='original aggregated output') OR
  (u.item_id='background-observation' AND u.output='output from retained observation')
 )`, thread, turn, source) != 2 {
		t.Fatal("interruption did not retain both original backgrounded outputs")
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses u JOIN conversation_messages m USING(message_id)
 WHERE u.thread_id=? AND u.turn_id=? AND m.reply_to=? AND u.item_id='with-result' AND u.status='failed' AND u.output='original failure'`, thread, turn, source) != 1 {
		t.Fatal("original failure did not replace the interrupted projection")
	}
	if h.countWhere(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND turn_id=? AND item_id='unfinished'
 AND kind='item_started' AND json_extract(data_json,'$.status')='inProgress'`, thread, turn) != 1 {
		t.Fatal("projection did not preserve the original native start")
	}
	if h.countWhere(`SELECT COUNT(*) FROM events e JOIN conversation_tool_uses u ON e.object_id=u.message_id
 WHERE u.item_id='unfinished' AND e.kind='conversation.tool_use' AND json_extract(e.body_json,'$.status')='interrupted'`) != 1 {
		t.Fatal("interruption was not published once for the original visible tool")
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='unrelated' AND status='running'`) != 1 ||
		h.countWhere(`SELECT COUNT(*) FROM manager_projected_running_tools WHERE run_id=?`, as.RunID) != 0 {
		t.Fatal("stop changed an unrelated tool or left settled projection tracking")
	}
}

func TestFault_ManagerProjectionPreservesNewestContextUsage(t *testing.T) {
	const originalThread = "original-thread"
	for _, tc := range []struct {
		name, existingThread            string
		existingRevision, eventRevision int64
		existingWrittenLater            bool
		wantThread                      string
		wantRevision                    int64
	}{
		{"replace_prior_thread_snapshot", "other-thread", 1, 1, false, originalThread, 1},
		{"preserve_later_thread_snapshot", "other-thread", 1, 1, true, "other-thread", 1},
		{"restore_higher_revision_received_before_live_write", originalThread, 1, 2, true, originalThread, 2},
		{"preserve_higher_revision_written_before_receipt", originalThread, 2, 1, false, originalThread, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			ctx := t.Context()
			id := as.Manager.ConversationID
			existing := protocol.AgentContextUsage{ThreadID: tc.existingThread, Revision: tc.existingRevision}
			if !tc.existingWrittenLater {
				if err := h.app.RecordAgentUsage(ctx, id, existing); err != nil {
					t.Fatal(err)
				}
			}
			h.clock.Advance(time.Second)
			raw, err := json.Marshal(managerProjectionEvent{ThreadID: originalThread, Native: &managerProjectionItem{
				closedManagerItem: closedManagerItem{Kind: "context_usage"},
				ContextUsage:      &protocol.AgentContextUsage{ThreadID: originalThread, Revision: tc.eventRevision},
			}})
			if err != nil {
				t.Fatal(err)
			}
			event := protocol.ManagerNativeEvent{Sequence: 1, Body: raw}
			if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if tc.existingWrittenLater {
				h.clock.Advance(time.Second)
				if err := h.app.RecordAgentUsage(ctx, id, existing); err != nil {
					t.Fatal(err)
				}
			}
			if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original stopped", LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.app.projectManagerNativeEvent(ctx, as.RunID, 1); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				usage, err := store.ContextUsage(ctx, tx.Q(), id)
				if err == nil && (usage.ThreadID != tc.wantThread || usage.Revision != tc.wantRevision) {
					t.Errorf("context usage is %q revision %d, want %q revision %d", usage.ThreadID, usage.Revision, tc.wantThread, tc.wantRevision)
				}
				if err == nil && tc.existingWrittenLater && usage.UpdatedAt != h.clock.Now().UnixMilli() {
					t.Errorf("context usage timestamp regressed to %d", usage.UpdatedAt)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			assertManagerProjectionWatermarks(t, h, as.RunID, 1, 1)
		})
	}
}
