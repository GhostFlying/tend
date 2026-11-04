package mainthread

import (
	"context"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type prefixThread struct {
	harness.Thread
	ack func(context.Context, harness.Event) error
}

func (*prefixThread) ID() string { return "native-original" }
func (t *prefixThread) AcknowledgeReplyPrefix(ctx context.Context, event harness.Event) error {
	return t.ack(ctx, event)
}

func TestFault_ManagerTerminalReplyAcknowledgesOnlyDurableDeltaPrefix(t *testing.T) {
	for _, state := range []protocol.AssistantState{protocol.AssistantInterrupted, protocol.AssistantUnknown} {
		t.Run(string(state), func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			drafts := replyDrafts{}
			for i, text := range []string{"original", "尾部", "🙂"} {
				event := harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: "original-turn", ItemID: "original-item", Text: text, CoordinatorSequence: int64(17 + i)}
				drafts.begin(event, e.m)
				drafts.consume(event)
			}
			attempts := 0
			ackFailure := errors.New("original prefix acknowledgement unavailable")
			thread := &prefixThread{ack: func(_ context.Context, event harness.Event) error {
				attempts++
				rows := assistantItems(t, e)
				if len(rows) != 1 || rows[0].Body != "original尾部🙂" || rows[0].AssistantState != state {
					t.Fatalf("ack preceded durable terminal text: %+v", rows)
				}
				if event.CoordinatorSequence != 19 || event.Kind != harness.EventAgentMessageDelta || event.TurnID != "original-turn" || event.ItemID != "original-item" {
					t.Fatalf("prefix lost original last event identity: %+v", event)
				}
				if attempts == 1 {
					return ackFailure
				}
				return nil
			}}
			if err := e.m.flushReplies(t.Context(), thread, drafts, "", ""); err != nil || attempts != 0 {
				t.Fatalf("ordinary streaming flush acknowledged replayable prefix: attempts=%d err=%v", attempts, err)
			}
			failedCtx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := e.m.flushReplies(failedCtx, thread, drafts, "", state); err == nil || attempts != 0 {
				t.Fatalf("failed terminal save acknowledged prefix: attempts=%d err=%v", attempts, err)
			}
			if err := e.m.flushReplies(t.Context(), thread, drafts, "", state); !errors.Is(err, ackFailure) {
				t.Fatalf("terminal save did not preserve acknowledgement failure: %v", err)
			}
			if err := e.m.flushReplies(t.Context(), thread, drafts, "", state); err != nil || attempts != 2 {
				t.Fatalf("same durable prefix could not retry acknowledgement: attempts=%d err=%v", attempts, err)
			}
			if err := e.m.flushReplies(t.Context(), thread, drafts, "", state); err != nil || attempts != 2 {
				t.Fatalf("clean terminal prefix was acknowledged again: attempts=%d err=%v", attempts, err)
			}
		})
	}
}
