package traex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestModelsPreserveCatalogIdentityAndPage(t *testing.T) {
	f, s := newFake(t)
	f.on(methodModelList, func(raw json.RawMessage) any {
		var p ModelListParams
		if err := json.Unmarshal(raw, &p); err != nil || p.IncludeHidden || p.Limit != 100 {
			t.Errorf("model/list params = %s", raw)
		}
		if p.Cursor == nil {
			return map[string]any{"data": []any{map[string]any{"id": "picker-id", "model": "provider-model", "displayName": "A model",
				"modelProviderId": "provider", "defaultReasoningEffort": "medium", "isDefault": true,
				"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "medium"}}}}, "nextCursor": "page-2"}
		}
		if *p.Cursor != "page-2" {
			t.Errorf("cursor = %q", *p.Cursor)
		}
		return map[string]any{"data": []any{map[string]any{"id": "fixed", "model": "fixed-name", "displayName": "Fixed",
			"defaultReasoningEffort": "high", "supportedReasoningEfforts": []any{}}}, "nextCursor": nil}
	})
	models, err := s.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []harness.Model{
		{ID: "picker-id", Name: "provider-model", Label: "A model", Provider: "provider", Default: true,
			DefaultEffort: "medium", SupportedEfforts: []string{"low", "medium"}},
		{ID: "fixed", Name: "fixed-name", Label: "Fixed", DefaultEffort: "high", SupportedEfforts: []string{}},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
}

func TestThreadDefaultsAndTurnOverrides(t *testing.T) {
	for _, mode := range []string{"start", "resume"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFake(t)
			response := map[string]any{"thread": map[string]any{"id": "thread"}, "model": "resolved-model", "modelProvider": "provider", "reasoningEffort": "high"}
			f.on(methodThreadStart, func(json.RawMessage) any { return response })
			f.on(methodThreadResume, func(json.RawMessage) any { return response })
			f.on(methodTurnStart, func(json.RawMessage) any { return map[string]any{"turn": map[string]any{"id": "turn"}} })
			var th harness.Thread
			var err error
			if mode == "resume" {
				th, err = s.ResumeThread(context.Background(), "thread", harness.ThreadConfig{})
			} else {
				th, err = s.StartThread(context.Background(), harness.ThreadConfig{Model: "requested-alias"})
			}
			if err != nil {
				t.Fatal(err)
			}
			want := harness.ModelSelection{Model: "resolved-model", Provider: "provider", Effort: "high"}
			if got := th.(harness.ConfiguredThread).ModelDefaults(); got != want {
				t.Fatalf("defaults = %+v, want %+v", got, want)
			}
			if _, err := th.StartTurn(context.Background(), harness.TurnInput{Text: "hello", Model: "another", ModelProvider: "other-provider", Effort: "low"}); err != nil {
				t.Fatal(err)
			}
			var got TurnStartParams
			if err := json.Unmarshal(f.params(methodTurnStart), &got); err != nil || got.Model != "another" || got.ModelProvider != "other-provider" || got.Effort != "low" {
				t.Fatalf("turn settings = %+v, %v", got, err)
			}
		})
	}
}

func TestAgentDeltasKeepItemAndThreadOrder(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any {
		// A fragment can arrive before the thread/start response.
		f.send(map[string]any{"method": notifyAgentMessageDelta, "params": AgentMessageDeltaNotification{ThreadID: "thread", TurnID: "turn", ItemID: "item", Delta: "你"}})
		return map[string]any{"thread": map[string]any{"id": "thread"}}
	})
	th, err := s.StartThread(context.Background(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	f.send(map[string]any{"method": notifyAgentMessageDelta, "params": AgentMessageDeltaNotification{ThreadID: "thread", TurnID: "turn", ItemID: "item", Delta: "好"}})
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "thread", "turnId": "turn",
		"item": map[string]any{"id": "item", "type": "agentMessage", "text": "你好"}}})
	for _, fragment := range []string{"你", "好"} {
		ev := next(t, th.Events())
		if ev.Kind != harness.EventAgentMessageDelta || ev.ItemType != harness.ItemAgentMessage || ev.TurnID != "turn" || ev.ItemID != "item" || ev.Text != fragment {
			t.Fatalf("delta = %+v, want %q", ev, fragment)
		}
	}
	if ev := next(t, th.Events()); ev.Kind != harness.EventItemCompleted || ev.Text != "你好" || ev.ItemID != "item" {
		t.Fatalf("final message = %+v", ev)
	}
}
