package mainthread

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestMessagingNativeReadIsVisibleBeforeTurnCompletion(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turn, ItemID: "native-user", ItemType: harness.ItemUserMessage, Text: in.Text})
		<-release
		return "completed"
	}
	e := newEnv(t, h)
	id := e.say("A request that remains in progress")
	eventually(t, "native read is durable while the turn is still running", func() bool {
		receipts, err := e.app.MessagingReceipts(t.Context(), id)
		return err == nil && len(receipts.Reads) == 1
	})
	if e.count(`SELECT count(*) FROM main_turns WHERE message_id=? AND status='started'`, id) != 1 {
		t.Fatal("read was delayed until completion")
	}
	if e.count(`SELECT count(*) FROM conversation_messages WHERE reply_to=? AND author='main_agent'`, id) != 0 {
		t.Fatal("unexpected reply")
	}
	once.Do(func() { close(release) })
}

func TestMessagingRepairPromptDoesNotProveOriginalRead(t *testing.T) {
	h := testkit.NewHarness()
	h.BeforeAnswer = func(th *testkit.FakeThread, turn string) {
		starts := h.Starts()
		if len(starts) == 0 {
			return
		}
		in := starts[len(starts)-1]
		if in.Repair {
			th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turn, ItemID: "repair-user", ItemType: harness.ItemUserMessage, Text: in.Text})
		}
	}
	e := newEnv(t, h)
	id := e.say("An original whose consumption was not witnessed")
	eventually(t, "metadata repair finished", func() bool {
		return e.count(`SELECT count(*) FROM logical_turn_executions WHERE repair=1 AND settled=1`) > 0
	})
	receipts, err := e.app.MessagingReceipts(t.Context(), id)
	if err != nil || len(receipts.Reads) != 0 {
		t.Fatal("repair prompt fabricated original read", receipts, err)
	}
}

func TestMessagingRecoveredNativeReadRequiresFrozenInput(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "changed"}[changed], func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			source := e.say("Original consumed before server crash")
			d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			session, err := e.h.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			base, err := session.StartThread(t.Context(), harness.ThreadConfig{})
			if err != nil {
				t.Fatal(err)
			}
			th := &receiptThread{Thread: base, found: true}
			if err := e.app.RegisterAgentThread(t.Context(), "main", th.ID()); err != nil {
				t.Fatal(err)
			}
			rec := store.MainTurn{RequestID: d.RequestID, MessageID: source, ThreadID: th.ID(), Status: store.TurnStarting}
			if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			text := "[message " + source + "]\nOriginal consumed before server crash"
			if err := e.app.RememberMessagingNativeInput(t.Context(), d.RequestID, source, text); err != nil {
				t.Fatal(err)
			}
			rec.TurnID, rec.Status = "known", store.TurnStarted
			if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			if err := e.app.SettleOpenTurns(t.Context(), th.ID()); err != nil {
				t.Fatal(err)
			}
			if changed {
				text += " altered"
			}
			th.receipt = harness.TurnReceipt{ID: rec.TurnID, Status: "completed", Items: []harness.Event{{TurnID: rec.TurnID, ItemID: "user-original", ItemType: harness.ItemUserMessage, Text: text}, {TurnID: rec.TurnID, ItemID: "final", ItemType: harness.ItemAgentMessage, Text: "Finished"}}}
			if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
				t.Fatal(err)
			}
			r, err := e.app.MessagingReceipts(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if changed {
				expected = 0
			}
			if len(r.Reads) != expected {
				t.Fatal(r)
			}
		})
	}
}

func TestMessagingSteeredCorrectionPreservesIdentity(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		started <- struct{}{}
		<-release
		return "completed"
	}
	inputs := make(chan harness.TurnInput, 1)
	e := newEnv(t, h, withSteer(h, func(_ context.Context, _ harness.Thread, _ string, in harness.TurnInput) error {
		inputs <- in
		return nil
	}))
	original := e.say("Use the original value")
	<-started
	eventually(t, "native turn started", func() bool { e.m.mu.Lock(); defer e.m.mu.Unlock(); return e.m.active != nil && e.m.active.turnID != "" })
	e.exec(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, "messaging.send", protocol.Target{Type: "message"}, protocol.MessagingInput{Body: "Use the corrected value", CorrectionOf: original}, app.MessagingCommand{Action: "send"})
	select {
	case in := <-inputs:
		if !strings.Contains(in.Text, "Tend correction of message "+original) {
			t.Fatalf("steered edit lost correction identity: %s", in.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("correction was not steered")
	}
}

func TestMessagingInstructionsReachEveryNamedRole(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		t.Run(string(purpose), func(t *testing.T) {
			m := &Manager{}
			m.named = &hostedconfig.Selection{Purpose: purpose}
			cfg := m.threadConfig()
			if !strings.Contains(cfg.DeveloperInstructions, NamedCommunicationInstructions) {
				t.Fatal("missing communication contract")
			}
			for _, name := range []string{"conversation_reply", "conversation_notify", "conversation_input_read"} {
				found := false
				for _, tool := range cfg.Tools {
					if tool.Name == name {
						found = true
					}
				}
				if !found {
					t.Fatalf("contract refers to absent tool %s", name)
				}
			}
		})
	}
}
