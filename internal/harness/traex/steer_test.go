package traex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestNativeSteerFencesTurnAndRequiresMatchingReceipt(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return ThreadStartResponse{Thread: Thread{ID: "thread"}} })
	f.on("turn/steer", func(json.RawMessage) any { return TurnSteerResponse{TurnID: "turn"} })
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	native := th.(harness.TurnSteerer)
	if err := native.SteerTurn(t.Context(), "", harness.TurnInput{Text: "input"}); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("unfenced steer was sent")
	}
	if err := native.SteerTurn(t.Context(), "turn", harness.TurnInput{Text: "input", ImageURLs: []string{"data:image/png;base64,AAAA"}}); err != nil {
		t.Fatal(err)
	}
	var p TurnSteerParams
	if err := json.Unmarshal(f.params("turn/steer"), &p); err != nil || p.ThreadID != "thread" || p.ExpectedTurnID != "turn" || len(p.Input) != 2 {
		t.Fatalf("native params=%+v err=%v", p, err)
	}
	if f.params(methodTurnStart) != nil {
		t.Fatal("steer started another turn")
	}
	f.on("turn/steer", func(json.RawMessage) any { return TurnSteerResponse{TurnID: "other"} })
	if err := native.SteerTurn(context.Background(), "turn", harness.TurnInput{Text: "input"}); err == nil || errors.Is(err, harness.ErrRejected) {
		t.Fatal("mismatched acceptance is not unknown")
	}
}
