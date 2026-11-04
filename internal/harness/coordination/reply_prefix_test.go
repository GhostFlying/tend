package coordination

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

type prefixTransport struct {
	Transport
	got      Event
	consumed []int64
	err      error
}

func (t *prefixTransport) AcknowledgeReplyPrefix(_ context.Context, event Event, consumed []int64) error {
	t.got = event
	t.consumed = consumed
	return t.err
}

func TestReplyPrefixRetainsOriginalDeltaWithoutInventingCompletion(t *testing.T) {
	failure := errors.New("prefix receipt unavailable")
	transport := &prefixTransport{err: failure}
	thread := &thread{session: &Session{transport: transport}, info: ThreadInfo{ID: "original-thread"}}
	event := harness.Event{Kind: harness.EventAgentMessageDelta, CoordinatorSequence: 19, TurnID: "original-turn", ItemID: "original-item"}
	if err := thread.AcknowledgeEvent(t.Context(), event); err != nil || transport.got.Sequence != 0 {
		t.Fatal("ordinary delta acknowledgement bypassed durable reply boundary", err)
	}
	if err := thread.AcknowledgeReplyPrefix(t.Context(), event, []int64{17, 18, 19}); !errors.Is(err, failure) {
		t.Fatal("lost prefix receipt error", err)
	}
	if got := transport.got; got.Sequence != 19 || got.ThreadID != "original-thread" || got.Disconnected || got.Native == nil || !reflect.DeepEqual(*got.Native, event) || !reflect.DeepEqual(transport.consumed, []int64{17, 18, 19}) {
		t.Fatalf("reply prefix invented a different native event: %+v", got)
	}
}
