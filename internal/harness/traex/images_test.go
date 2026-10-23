package traex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestNativeImageTurnUsesImageVariant(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return ThreadStartResponse{Thread: Thread{ID: "th_image"}} })
	f.on(methodTurnStart, func(json.RawMessage) any { return TurnStartResponse{Turn: Turn{ID: "turn_image"}} })
	th, err := s.StartThread(context.Background(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = th.StartTurn(context.Background(), harness.TurnInput{Text: "describe", ImageURLs: []string{"data:image/png;base64,AAAA"}, UserMessageID: "msg_image"})
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(f.params(methodTurnStart), &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Input) != 2 || params.Input[0]["text"] != "describe" || params.Input[1]["type"] != "image" || params.Input[1]["url"] != "data:image/png;base64,AAAA" || len(params.Input[1]) != 2 {
		t.Fatalf("native input=%s", f.params(methodTurnStart))
	}
}
