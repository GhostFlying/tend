package mainthread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestDeferredReplyDiagnosticsUseEachDraftIdentity(t *testing.T) {
	for _, path := range []string{"ticker_context", "closing_cleanup_context", "other_event_context"} {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			drafts := replyDrafts{}
			for n, origin := range []string{"first-original-run", "second-original-run"} {
				ev := harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: "original-turn", ItemID: origin, Text: origin + " retained text", OriginRunID: origin, OriginSequence: 14, CoordinatorSequence: int64(n + 1), AttachmentRunID: "original-reader"}
				drafts.begin(ev, e.m)
				drafts.consume(ev)
			}
			var observed []store.WriteObservation
			store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observed = append(observed, o) })(e.store)
			prefixACKs := 0
			thread := &prefixThread{ack: func(ctx context.Context, ev harness.Event, _ []int64) error {
				prefixACKs++
				return e.store.Write(store.WithWriteOperation(ctx, "test.prefix.ack"), func(*store.Tx) error { return nil })
			}}
			ctx := store.WithWriteSource(t.Context(), store.WriteSource{RequestID: "enclosing-request", TraceID: "enclosing-trace"})
			if path == "other_event_context" {
				ctx = store.WithWriteEvent(store.WithWriteAttachment(ctx, "wrong-reader", 99), "wrong-event-run", 100)
			}
			var err error
			if path == "closing_cleanup_context" {
				err = e.m.flushClosingReplies(ctx, thread, drafts, protocol.AssistantInterrupted)
			} else {
				err = e.m.flushReplies(ctx, thread, drafts, "", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(observed) == 0 {
				t.Fatal("no actual reply writes observed")
			}
			seen := map[string]bool{}
			for _, o := range observed {
				want := int64(1)
				if o.Source.RunID == "second-original-run" {
					want = 2
				} else if o.Source.RunID != "first-original-run" {
					t.Fatalf("draft inherited flush-trigger scope: %+v", o.Source)
				}
				if o.Source.EventSequence != 14 || o.Source.CoordinatorSequence != want || o.Source.AttachmentRunID != "original-reader" {
					t.Fatalf("draft lost source/attachment scope: %+v", o.Source)
				}
				seen[o.Source.RunID] = true
				if o.Source.Operation == "test.prefix.ack" && (o.Source.RequestID != "enclosing-request" || o.Source.TraceID != "enclosing-trace") {
					t.Fatalf("closing ACK replaced enclosing request: %+v", o.Source)
				}
				raw, _ := json.Marshal(o)
				if strings.Contains(string(raw), "retained text") {
					t.Fatalf("reply body leaked into diagnostics: %s", raw)
				}
			}
			if len(seen) != 2 {
				t.Fatalf("did not persist both origins: %v", seen)
			}
			wantACKs := 0
			if path == "closing_cleanup_context" {
				wantACKs = 2
			}
			if prefixACKs != wantACKs {
				t.Fatalf("prefix ACK behavior changed: %d", prefixACKs)
			}
			rows := assistantItems(t, e)
			if len(rows) != 2 || rows[0].Body != "first-original-run retained text" || rows[1].Body != "second-original-run retained text" {
				t.Fatalf("reply output/order changed: %+v", rows)
			}
		})
	}
}
