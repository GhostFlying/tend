package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/harness"
)

func (t *thread) FindTurn(ctx context.Context, identity, knownTurn string) (harness.TurnReceipt, bool, error) {
	var raw json.RawMessage
	if err := t.s.conn.call(ctx, "thread/read", map[string]any{"threadId": t.id, "includeTurns": true}, &raw); err != nil {
		return harness.TurnReceipt{}, false, err
	}
	var response struct {
		Thread struct {
			Turns []json.RawMessage `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return harness.TurnReceipt{}, false, err
	}
	var receipt harness.TurnReceipt
	found := false
	for _, turnRaw := range response.Thread.Turns {
		var turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Items  []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"items"`
		}
		if err := json.Unmarshal(turnRaw, &turn); err != nil {
			return harness.TurnReceipt{}, false, err
		}
		matches := knownTurn != "" && turn.ID == knownTurn
		for _, item := range turn.Items {
			if knownTurn == "" && item.Type == harness.ItemUserMessage && item.ID == identity {
				matches = true
			}
		}
		if !matches {
			continue
		}
		if found {
			return harness.TurnReceipt{}, false, fmt.Errorf("native history contains more than one matching execution")
		}
		found = true
		receipt = harness.TurnReceipt{ID: turn.ID, Status: turn.Status, Raw: turnRaw}
		for _, item := range turn.Items {
			if item.Type == harness.ItemAgentMessage {
				receipt.Messages = append(receipt.Messages, harness.Event{Kind: harness.EventItemCompleted, TurnID: turn.ID, ItemID: item.ID, ItemType: item.Type, Text: item.Text})
			}
		}
	}
	return receipt, found, nil
}
