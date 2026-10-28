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
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
			Items  []struct {
				ID       string `json:"id"`
				ClientID string `json:"clientId"`
				Type     string `json:"type"`
				Text     string `json:"text"`
				Content  []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"items"`
		}
		if err := json.Unmarshal(turnRaw, &turn); err != nil {
			return harness.TurnReceipt{}, false, err
		}
		matches := knownTurn != "" && turn.ID == knownTurn
		for _, item := range turn.Items {
			if knownTurn == "" && item.Type == harness.ItemUserMessage && identity != "" && ((!t.s.codex && item.ID == identity) || (t.s.codex && item.ClientID == identity)) {
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
		receipt = harness.TurnReceipt{ID: turn.ID, Status: turn.Status, Raw: turnRaw, Failure: nativeFailure(turn.Error, false), ContinuationSafe: t.s.codex && continuationSafe(turnRaw), Items: []harness.Event{}}
		for _, item := range turn.Items {
			ev := harness.Event{Kind: harness.EventItemCompleted, TurnID: turn.ID, ItemID: item.ID, ItemType: item.Type, Text: item.Text}
			if item.Type == harness.ItemUserMessage {
				ev.Text = ""
				for _, part := range item.Content {
					ev.Text += part.Text
				}
				receipt.Items = append(receipt.Items, ev)
			}
			if item.Type == harness.ItemAgentMessage {
				receipt.Messages = append(receipt.Messages, ev)
				receipt.Items = append(receipt.Items, ev)
			}
		}
	}
	return receipt, found, nil
}

// continuationSafe requires terminal native items, including exited commands.
func continuationSafe(raw json.RawMessage) bool {
	var turn struct {
		Status string `json:"status"`
		Items  []struct {
			Type     string `json:"type"`
			Status   string `json:"status"`
			ExitCode *int   `json:"exitCode"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &turn) != nil || turn.Status != "failed" {
		return false
	}
	for _, item := range turn.Items {
		switch item.Status {
		case "inProgress", "running", "pending", "unknown":
			return false
		}
		if item.Type == "commandExecution" && item.ExitCode == nil {
			return false
		}
		if item.Type == "collabAgentToolCall" {
			return false
		}
	}
	return true
}
