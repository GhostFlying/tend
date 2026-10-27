package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCompletionNoticeIsAtomicAndNotRepeated(t *testing.T) {
	h := newHarness(t)
	taskID, acc := h.toAcceptanceClaim()
	env := h.env(protocol.OpAcceptancesSubmit, runTarget(acc.RunID), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil)
	p := runPrincipal(acc.RunID, acc.Role)
	first := h.exec(p, env, SubmitAcceptance{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	if replay := h.exec(p, env, SubmitAcceptance{}); string(replay.Result) != string(first.Result) {
		t.Fatal("a replay changed the acceptance result")
	}
	// A new request reporting the finished acceptance cannot complete the
	// task a second time, even though its request identity is different.
	h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{})
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.ListConversation(t.Context(), tx.Q(), 0, 0, 100)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Author != "system" || rows[0].Kind != "message" || rows[0].ReplyTo != "" {
			t.Fatalf("completion items = %+v", rows)
		}
		metadata, err := store.GetMessageNotice(t.Context(), tx.Q(), rows[0].MessageID)
		if err != nil {
			return err
		}
		if metadata.NoticeType != "task_completed" {
			t.Fatalf("completion notice metadata = %+v", metadata)
		}
		body := rows[0].Body
		if !strings.Contains(body, taskID) || !strings.Contains(body, "spec 1") || !strings.Contains(body, commitSHA) {
			t.Fatalf("notice lost accepted facts: %s", body)
		}
		messages, err := store.RecentMessages(t.Context(), tx.Q(), 8, 0)
		if err != nil {
			return err
		}
		if len(messages) != 1 || messages[0].Author != "system" || len(messages[0].Bindings) != 5 {
			t.Fatalf("notice is not available to context recall: %+v", messages)
		}
		var turns int
		if err := tx.Q().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM outbox WHERE destination LIKE 'background_thread:%'").Scan(&turns); err != nil {
			return err
		}
		if turns != 1 {
			t.Fatalf("completion event turns = %d, want 1", turns)
		}
		var notices int
		if err := tx.Q().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM events WHERE kind='conversation.message'").Scan(&notices); err != nil {
			return err
		}
		if notices != 2 {
			t.Fatalf("notice events = %d", notices)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFault_CompletionNoticeFailureRollsBackAcceptance(t *testing.T) {
	h := newHarness(t)
	taskID, acc := h.toAcceptanceClaim()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_notice BEFORE INSERT ON conversation_messages
			WHEN NEW.author = 'system' BEGIN SELECT RAISE(ABORT, 'notice unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpAcceptancesSubmit, runTarget(acc.RunID), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil)
	if _, err := h.app.Execute(t.Context(), runPrincipal(acc.RunID, acc.Role), env, SubmitAcceptance{}); err == nil {
		t.Fatal("the transaction succeeded without the completion notice")
	}
	if task := h.task(taskID); task.Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatalf("failed transaction completed task: %+v", task)
	}
	if h.count("acceptances") != 0 || h.count("conversation_messages") != 0 {
		t.Fatal("failed transaction leaked acceptance or message")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), "DROP TRIGGER reject_notice")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runPrincipal(acc.RunID, acc.Role), env, SubmitAcceptance{}); op.Status != protocol.OpSucceeded || h.task(taskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("the original request did not recover after notice storage recovered")
	}
}

func TestNonPassingAcceptanceDoesNotAnnounceCompletion(t *testing.T) {
	for _, verdict := range []protocol.Verdict{protocol.VerdictChangesRequired, protocol.VerdictBlocked, protocol.VerdictInconclusive} {
		t.Run(string(verdict), func(t *testing.T) {
			h := newHarness(t)
			_, acc := h.toAcceptanceClaim()
			h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: verdict}, SubmitAcceptance{})
			if h.count("message_notices WHERE notice_type='task_completed'") != 0 {
				t.Fatal("non-passing verdict announced completion")
			}
		})
	}
}

func TestFault_StaleAcceptanceDoesNotAnnounceCompletion(t *testing.T) {
	h := newHarness(t)
	taskID, old := h.toAcceptanceClaim()
	spec := testSpec
	spec.Constraints = []string{"a new requirement"}
	h.exec(owner, h.env("tasks.update_intent", taskTarget(taskID), updateIntentPayload{Spec: &spec}, rev(h.task(taskID).ControlRevision)), UpdateIntent{})
	h.runAs(old, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{})
	if task := h.task(taskID); task.SpecVersion != 2 || task.Lifecycle != protocol.TaskQueued {
		t.Fatalf("late verdict changed the current task: %+v", task)
	}
	if h.count("conversation_messages") != 0 {
		t.Fatal("a stale passing verdict announced current completion")
	}
}
