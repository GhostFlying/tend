package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func lapsedReplyFixture(t *testing.T) (*harness, protocol.Assignment, string) {
	t.Helper()
	h, _, _, as, source := reviewManagerAssignment(t)
	selection, named, err := h.app.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	business := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: business, MessageID: source, ThreadID: "native", TurnID: "original-turn", Status: store.TurnInterrupted}, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: business, ConversationID: as.Manager.ConversationID, IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, business)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resume, err := h.app.QueueManagerNative(t.Context(), as.RunID, "resume", json.RawMessage(`{"thread_id":"native"}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: resume, Result: json.RawMessage(`{"ID":"native"}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for sequence := int64(1); sequence <= 19; sequence++ {
		body := json.RawMessage(`{"thread_id":"native","native":{"Kind":"request_usage"}}`)
		if sequence >= 17 {
			body = json.RawMessage(fmt.Sprintf(`{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":"original-item","Text":"part%d"}}`, sequence))
		}
		if op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: sequence, Body: body}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if sequence < 17 {
			if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, sequence, "native", "", "", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	close, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE leases SET status='lapsed' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return h, as, close
}

func TestFault_LapsedManagerReplaysOnlyItsFrozenDatabasePrefix(t *testing.T) {
	h, as, close := lapsedReplyFixture(t)
	scope, err := h.app.LapsedManagerReplyReplay(t.Context(), as.Manager.ConversationID)
	if err != nil || scope == nil {
		t.Fatalf("lapsed closing manager has no database reply consumer: scope=%v err=%v", scope, err)
	}
	if scope.RunID != as.RunID || scope.LeaseID != as.LeaseID || scope.ThreadID != "native" || !scope.Selection.ReceiptOnly || len(scope.Events) != 3 || scope.Events[0].Sequence != 17 || scope.Events[2].Sequence != 19 {
		t.Fatal("replay changed original identity or read boundary", scope)
	}
	if count := h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE method='models'`); count != 0 {
		t.Fatal("fixture unexpectedly requires model discovery", count)
	}
	claim := h.env(protocol.OpManagerNativeClaim, runTarget(as.RunID), map[string]any{}, nil)
	if _, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), claim, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("database replay reopened native claim under a lapsed lease", err)
	}
	frozen := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{ID: close, Result: json.RawMessage(`null`), LastSequence: 19}, nil)
	principal := runPrincipal(as.RunID, as.Role)
	if _, err := h.app.Execute(t.Context(), principal, frozen, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("lapsed close bypassed unapplied prefix", err)
	}
	var text strings.Builder
	var consumed []int64
	for _, event := range scope.Events {
		var raw struct{ Native struct{ Text string } }
		if err := json.Unmarshal(event.Body, &raw); err != nil {
			t.Fatal(err)
		}
		text.WriteString(raw.Native.Text)
		consumed = append(consumed, event.Sequence)
	}
	recordLapsedReply(t, h, text.String(), RecordAgentMessage{PreservePrefix: true})
	if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", text.String(), consumed); err != nil {
		t.Fatal(err)
	}
	if result, err := h.app.Execute(t.Context(), principal, frozen, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); err != nil || result.Status != protocol.OpSucceeded {
		t.Fatal("original immutable close cannot commit after durable replay", result, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pending, err := managerBusinessReceiptsPending(t.Context(), tx.Q(), as.RunID)
		if err == nil && !pending {
			t.Fatal("physical close fabricated a business receipt")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func recordLapsedReply(t *testing.T, h *harness, body string, command RecordAgentMessage) {
	t.Helper()
	p := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
	env := h.env(command.Operation(), protocol.Target{Type: "message"}, map[string]any{"body": body, "turn_id": "original-turn", "item_id": "original-item", "status": protocol.AssistantInterrupted}, nil)
	if result := h.exec(p, env, command); result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
}

func TestLapsedManagerReplayPreservesScopeAndActiveSelection(t *testing.T) {
	for _, change := range []string{"active", "released", "no_pending", "no_close", "pin", "lease_host", "epoch", "frozen_model", "resume", "event_turn", "non_reply"} {
		t.Run(change, func(t *testing.T) {
			h, as, _ := lapsedReplyFixture(t)
			if change == "lease_host" {
				cfg := testConfig()
				cfg.Hosts = append(cfg.Hosts, cfg.Hosts[0])
				cfg.Hosts[1].HostID = "another-host"
				if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				var err error
				switch change {
				case "active", "released":
					_, err = tx.Exec(t.Context(), `UPDATE leases SET status=? WHERE run_id=?`, change, as.RunID)
				case "no_pending":
					_, err = tx.Exec(t.Context(), `DELETE FROM native_turn_receipts_pending`)
				case "no_close":
					_, err = tx.Exec(t.Context(), `DELETE FROM manager_native_requests WHERE method='close'`)
				case "pin":
					_, err = tx.Exec(t.Context(), `UPDATE issue_manager_tasks SET pin_json=json_set(pin_json,'$.agent_revision',99) WHERE task_id=?`, as.TaskID)
				case "lease_host":
					_, err = tx.Exec(t.Context(), `UPDATE leases SET host_id='another-host' WHERE run_id=?`, as.RunID)
				case "epoch":
					_, err = tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET assignment_epoch=assignment_epoch+1`)
				case "frozen_model":
					_, err = tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET private_snapshot=json_set(private_snapshot,'$.definition.model','replacement')`)
				case "resume":
					_, err = tx.Exec(t.Context(), `UPDATE manager_native_requests SET result_json=json_set(result_json,'$.result.ID','another-thread') WHERE method='resume'`)
				case "event_turn":
					_, err = tx.Exec(t.Context(), `UPDATE manager_native_events SET body_json=json_set(body_json,'$.native.TurnID','another-turn') WHERE seq=18`)
				case "non_reply":
					_, err = tx.Exec(t.Context(), `UPDATE manager_native_events SET body_json=json_set(body_json,'$.native.Kind','turn_completed') WHERE seq=18`)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			scope, err := h.app.LapsedManagerReplyReplay(t.Context(), as.Manager.ConversationID)
			if scope != nil {
				t.Fatal("ineligible responsibility entered database replay", scope)
			}
			if change == "active" {
				selection, named, readErr := h.app.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
				if err != nil || readErr != nil || !named || selection.RunnerRunID != as.RunID || !selection.ReceiptOnly {
					t.Fatal("ordinary active closing attachment changed", selection, named, readErr, err)
				}
			}
		})
	}
}

func (h *harness) countWhere(query string, args ...any) int {
	h.t.Helper()
	var count int
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(h.t.Context(), query, args...).Scan(&count)
	}); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func TestDatabaseReplayCannotOverwriteRecordedPrefix(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint(background), func(t *testing.T) {
			h := newHarness(t)
			principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
			payload := map[string]any{"body": "existing prefix", "turn_id": "original-turn", "item_id": "original-item", "status": protocol.AssistantStreaming}
			if background {
				work := h.createWork(workPlan())
				h.followup(work, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
				_, turn := h.startBackground()
				principal = mainAgent
				payload["reply_to"], payload["turn_id"] = turn.MessageID, turn.TurnID
			}
			if op := h.exec(principal, h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, payload, nil), RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			payload["body"], payload["status"] = "remaining suffix", protocol.AssistantInterrupted
			env := h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, payload, nil)
			if _, err := h.app.Execute(t.Context(), principal, env, RecordAgentMessage{PreservePrefix: true}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
				t.Fatal("database replay replaced saved text with a suffix", err)
			}
		})
	}
}

func TestLapsedManagerReplayRetainsAppliedFragmentsAndItsReadBoundary(t *testing.T) {
	h, as, _ := lapsedReplyFixture(t)
	if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, 17, "native", "original-turn", "original-item", false); err != nil {
		t.Fatal(err)
	}
	scope, err := h.app.LapsedManagerReplyReplay(t.Context(), as.Manager.ConversationID)
	if err != nil || scope == nil || len(scope.Events) != 3 || scope.Events[0].Sequence != 17 {
		t.Fatal("applied original prefix was lost", scope, err)
	}
	late := protocol.ManagerNativeEvent{Sequence: 20, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":"original-item","Text":"later"}}`)}
	if op := h.runAs(as, protocol.OpManagerNativeEvent, late, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal("late original event was rejected after lapse", op)
	}
	if len(scope.Events) != 3 || scope.Events[2].Sequence != 19 {
		t.Fatal("later event changed an existing read boundary", scope.Events)
	}
	recordLapsedReply(t, h, "part17part18part19", RecordAgentMessage{PreservePrefix: true})
	if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "part17part18part19", []int64{17, 18, 19}); err != nil {
		t.Fatal(err)
	}
	if count := h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq=20 AND applied=0`, as.RunID); count != 1 {
		t.Fatal("fixed prefix acknowledged a later event", count)
	}
}

func TestDatabaseReplayExtendsOnlyInterruptedRecordedPrefixes(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, state := range []protocol.AssistantState{protocol.AssistantInterrupted, protocol.AssistantCompleted} {
			t.Run(fmt.Sprint(background, "/", state), func(t *testing.T) {
				h := newHarness(t)
				principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
				payload := map[string]any{"body": "original", "turn_id": "original-turn", "item_id": "original-item", "status": state}
				if background {
					work := h.createWork(workPlan())
					h.followup(work, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
					_, turn := h.startBackground()
					principal = mainAgent
					payload["reply_to"], payload["turn_id"] = turn.MessageID, turn.TurnID
				}
				command := RecordAgentMessage{PreservePrefix: true}
				if op := h.exec(principal, h.env(command.Operation(), protocol.Target{Type: "message"}, payload, nil), command); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				payload["body"], payload["status"] = "original late tail", protocol.AssistantInterrupted
				if op := h.exec(principal, h.env(command.Operation(), protocol.Target{Type: "message"}, payload, nil), command); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				want := "original"
				if state == protocol.AssistantInterrupted {
					want = "original late tail"
				}
				query := `SELECT m.body FROM conversation_agent_messages a JOIN conversation_messages m USING(message_id) WHERE a.thread_id=? AND a.turn_id=? AND a.item_id=?`
				if background {
					query = `SELECT body FROM background_replies WHERE thread_id=? AND turn_id=? AND item_id=?`
				}
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var body string
					if err := tx.Q().QueryRowContext(t.Context(), query, principal.Thread, payload["turn_id"], payload["item_id"]).Scan(&body); err != nil {
						return err
					}
					if body != want {
						t.Fatalf("replay extension changed terminal policy: got %q, want %q", body, want)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestDatabaseReplayAcknowledgesDurableBufferedTextWithoutSettlingBatch(t *testing.T) {
	h, as, _ := lapsedReplyFixture(t)
	work := h.createWork(workPlan())
	h.followup(work, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	turn := store.MainTurn{RequestID: h.ids.New(protocol.PrefixRequest), ThreadID: mainAgent.Thread, TurnID: "buffered-original-turn", Status: store.TurnStarted}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT message_id FROM background_batches`).Scan(&turn.MessageID); err != nil {
			return err
		}
		if err := tx.PutMainTurn(t.Context(), turn, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE background_batches SET status='frozen' WHERE message_id=?`, turn.MessageID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE manager_native_events SET body_json=json_set(body_json,'$.thread_id',?,'$.native.TurnID',?) WHERE run_id=? AND seq>=17`, turn.ThreadID, turn.TurnID, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	command := RecordAgentMessage{PreservePrefix: true}
	payload := map[string]any{"body": "part17part18part19", "reply_to": turn.MessageID, "turn_id": turn.TurnID, "item_id": "original-item", "status": protocol.AssistantStreaming}
	record := func() {
		t.Helper()
		if op := h.exec(mainAgent, h.env(command.Operation(), protocol.Target{Type: "message"}, payload, nil), command); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	ack := func(body string) error {
		return h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, turn.ThreadID, turn.TurnID, "original-item", body, []int64{17, 18, 19})
	}
	record()
	if err := ack(payload["body"].(string)); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("streaming buffer was treated as durable terminal text", err)
	}
	payload["status"] = protocol.AssistantInterrupted
	record()
	if err := ack("different text"); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("mismatched buffered body was acknowledged", err)
	}
	if err := ack(payload["body"].(string)); err != nil {
		t.Fatal("exact terminal background buffer could not acknowledge its original deltas", err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id=? AND turn_id=?`, turn.ThreadID, turn.TurnID) != 0 || h.countWhere(`SELECT COUNT(*) FROM background_batches WHERE message_id=? AND status='frozen'`, turn.MessageID) != 1 || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq>=17 AND applied=0`, as.RunID) != 0 || h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 {
		t.Fatal("buffer acknowledgement published output, settled business or lost the prefix", h.countWhere(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id=? AND turn_id=?`, turn.ThreadID, turn.TurnID), h.countWhere(`SELECT COUNT(*) FROM background_batches WHERE message_id=? AND status='frozen'`, turn.MessageID), h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq>=17 AND applied=0`, as.RunID), h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`))
	}
}

func TestDatabaseReplayExtendsPublishedInterruptedBackgroundPrefix(t *testing.T) {
	h := newHarness(t)
	work := h.createWork(workPlan())
	h.followup(work, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	command := RecordAgentMessage{PreservePrefix: true}
	payload := map[string]any{"body": "original", "reply_to": turn.MessageID, "turn_id": turn.TurnID, "item_id": "original-item", "status": protocol.AssistantStreaming}
	record := func() {
		t.Helper()
		if op := h.exec(mainAgent, h.env(command.Operation(), protocol.Target{Type: "message"}, payload, nil), command); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	record()
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM background_replies WHERE thread_id=? AND turn_id=? AND published=1`, turn.ThreadID, turn.TurnID) != 1 {
		t.Fatal("fixture did not publish its interrupted streaming prefix")
	}
	payload["body"], payload["status"] = "original late tail", protocol.AssistantInterrupted
	record()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return durableManagerReplyPrefix(t.Context(), tx.Q(), turn.ThreadID, turn.TurnID, "original-item", payload["body"].(string))
	}); err != nil {
		t.Fatal("published original prefix did not extend with its durable buffer", err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM main_turns WHERE request_id=? AND status='interrupted'`, turn.RequestID) != 1 || h.countWhere(`SELECT COUNT(*) FROM background_batches WHERE message_id=? AND status='interrupted'`, turn.MessageID) != 1 {
		t.Fatal("replaying original text changed its interrupted business outcome")
	}
}
