package traex

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"testing"
	"time"
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

func TestNestedChildEventsKeepIdentityAcrossLateAncestorRouting(t *testing.T) {
	for _, registeredFirst := range []bool{false, true} {
		rootName := "late root registration"
		if registeredFirst {
			rootName = "registered root"
		}
		t.Run(rootName, func(t *testing.T) {
			for _, order := range [][]string{
				{"grandchild", "usage", "tool", "child"},
				{"usage", "tool", "grandchild", "child"},
				{"child", "grandchild", "usage", "tool"},
				{"grandchild", "usage", "child", "tool"},
			} {
				t.Run(order[0]+"-"+order[1]+"-"+order[2]+"-"+order[3], func(t *testing.T) {
					_, s := newFake(t)
					var root *thread
					if registeredFirst {
						root = s.register("root", nil)
					}
					usage := json.RawMessage(`{"threadId":"grandchild","turnId":"g-turn","responseId":"g-response","usage":{"inputTokens":10,"outputTokens":2,"totalTokens":12}}`)
					events := map[string]struct {
						method string
						params json.RawMessage
					}{
						"grandchild": {"thread/started", json.RawMessage(`{"thread":{"id":"grandchild","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"child","depth":2}}}}}`)},
						"child":      {"thread/started", json.RawMessage(`{"thread":{"id":"child","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"root","depth":1}}}}}`)},
						"usage":      {"rawResponse/completed", usage},
						"tool":       {notifyItemStarted, json.RawMessage(`{"threadId":"grandchild","turnId":"g-turn","item":{"id":"g-tool","type":"commandExecution","command":"echo grandchild","status":"inProgress"}}`)},
					}
					for _, name := range order {
						n := events[name]
						s.notification(n.method, n.params)
					}
					// Identical replay after the complete lineage is known must not change
					// the usage fact or manufacture an attribution conflict downstream.
					s.notification("rawResponse/completed", usage)
					if root == nil {
						root = s.register("root", nil)
					}
					var samples []json.RawMessage
					tools := 0
					for range 3 {
						var event harness.Event
						select {
						case event = <-root.Events():
						case <-time.After(3 * time.Second):
							t.Fatal("nested event never reached its root")
						}
						if event.RequestUsage != nil {
							u := event.RequestUsage
							if u.ThreadID != "grandchild" || u.ParentThreadID != "child" || u.RequestID != "g-response" {
								t.Fatalf("native usage identity changed: %+v", u)
							}
							b, err := json.Marshal(u)
							if err != nil {
								t.Fatal(err)
							}
							samples = append(samples, b)
						} else {
							if event.Kind != harness.EventChildRollout {
								t.Fatalf("lost child rollout: %+v", event)
							}
							var envelope struct {
								Thread string            `json:"thread_id"`
								Turn   string            `json:"turn_id"`
								Kind   harness.EventKind `json:"kind"`
								Data   json.RawMessage   `json:"data"`
							}
							if err := json.Unmarshal(event.Raw, &envelope); err != nil {
								t.Fatal(err)
							}
							var item struct {
								ID      string `json:"id"`
								Type    string `json:"type"`
								Command string `json:"command"`
							}
							if err := json.Unmarshal(envelope.Data, &item); err != nil {
								t.Fatal(err)
							}
							if envelope.Thread != "grandchild" || envelope.Turn != "g-turn" || envelope.Kind != harness.EventItemStarted || item.ID != "g-tool" || item.Type != itemCommand || item.Command != "echo grandchild" {
								t.Fatalf("nested envelope changed native tool identity: %s", event.Raw)
							}
							tools++
						}
					}
					if len(samples) != 2 || string(samples[0]) != string(samples[1]) || tools != 1 {
						t.Fatalf("replay changed the native fact: samples=%s tools=%d", samples, tools)
					}
				})
			}
		})
	}
}
