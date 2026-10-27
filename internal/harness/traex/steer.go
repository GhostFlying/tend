package traex

import (
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// TurnSteerParams is fenced to the current active turn (TraeX 0.211.1).
type TurnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	ExpectedTurnID string      `json:"expectedTurnId"`
	Input          []UserInput `json:"input"`
}

// TurnSteerResponse proves native acceptance only.
type TurnSteerResponse struct {
	TurnID string `json:"turnId"`
}

var _ harness.TurnSteerer = (*thread)(nil)

func (t *thread) SteerTurn(ctx context.Context, expectedTurnID string, in harness.TurnInput) error {
	if expectedTurnID == "" || t.toolsReleased() {
		return fmt.Errorf("%w: steer requires a live turn", harness.ErrRejected)
	}
	inputs := []UserInput{{Type: userInputText, Text: in.Text, TextElements: []any{}}}
	for _, image := range in.ImageURLs {
		inputs = append(inputs, UserInput{Type: "image", imageURL: image})
	}
	var res TurnSteerResponse
	if err := t.s.conn.call(ctx, "turn/steer", TurnSteerParams{ThreadID: t.id, ExpectedTurnID: expectedTurnID, Input: inputs}, &res); err != nil {
		var rpc *RPCError
		if errors.As(err, &rpc) && rpc.Code == -32601 {
			return fmt.Errorf("%w: %w", harness.ErrSteerUnsupported, err)
		}
		if errors.As(err, &rpc) && rpc.Code != -32600 && rpc.Code != -32601 && rpc.Code != -32602 {
			// Internal server failures do not prove input was not queued. Do
			// not expose RPCError's generic ErrRejected classification here.
			return fmt.Errorf("traex: steer outcome unknown after native RPC error %d", rpc.Code)
		}
		return err
	}
	if res.TurnID != expectedTurnID {
		return fmt.Errorf("traex: steer acceptance did not name the expected turn")
	}
	return nil
}
