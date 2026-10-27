package traex

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"testing"
)

func TestRequestUsagePreservesMissingCacheDetailAndNativeRequestIdentity(t *testing.T) {
	_, s := newFake(t)
	for _, test := range []struct {
		name, metadata string
		read, write    bool
	}{
		{"missing", "null", false, false},
		{"explicit zero", `{"metadata":{"input_tokens_details":{"cached_tokens":0}}}`, true, false},
		{"write provided", `{"metadata":{"input_tokens_details":{"cached_tokens":6,"cache_write_tokens":2}}}`, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "thread-" + test.name
			s.notification("rawResponse/completed", json.RawMessage(`{"threadId":"`+id+`","turnId":"turn","responseId":"response","usage":{"inputTokens":10,"cachedInputTokens":0,"cacheCreationInputTokens":0,"outputTokens":2,"totalTokens":12},"usageMetadata":`+test.metadata+`}`))
			s.mu.Lock()
			events := s.orphans[id]
			s.mu.Unlock()
			if len(events) != 1 || events[0].Kind != harness.EventRequestUsage {
				t.Fatal(events)
			}
			u := events[0].RequestUsage
			if u == nil || u.RequestID != "response" || u.ThreadID != id || u.Total == nil || *u.Total != 12 || u.OccurredAt != nil || u.Model != "" {
				t.Fatal(u)
			}
			if (u.CacheRead != nil) != test.read || (u.CacheWrite != nil) != test.write {
				t.Fatalf("cache presence invented/lost: %+v", u)
			}
		})
	}
}

func TestChildRequestUsesExplicitNativeLineageWithoutTurnCumulative(t *testing.T) {
	_, s := newFake(t)
	// A completion can arrive before the child-start notification; it stays in
	// the bounded orphan queue until native lineage establishes its ownership.
	s.notification("rawResponse/completed", json.RawMessage(`{"threadId":"child","turnId":"child-turn","responseId":"child-response","usage":{"inputTokens":10,"outputTokens":2,"totalTokens":12}}`))
	s.notification("thread/started", json.RawMessage(`{"thread":{"id":"child","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}}}`))
	s.notification("turn/completed", json.RawMessage(`{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}`))
	s.mu.Lock()
	events := s.orphans["parent"]
	child := len(s.orphans["child"])
	s.mu.Unlock()
	if child != 0 || len(events) != 2 || events[0].RequestUsage == nil || events[0].RequestUsage.ParentThreadID != "parent" || events[1].Kind != harness.EventChildRollout {
		t.Fatal(events, child)
	}
	// Descendant-inclusive tokenUsage totals are context/checkpoint data and
	// cannot add another request contribution.
	s.notification("thread/tokenUsage/updated", json.RawMessage(`{"threadId":"child","turnId":"child-turn","tokenUsage":{"total":{"totalTokens":999}}}`))
	s.mu.Lock()
	n := len(s.orphans["parent"])
	s.mu.Unlock()
	if n != 2 {
		t.Fatal("counted a cumulative usage notification")
	}
}
