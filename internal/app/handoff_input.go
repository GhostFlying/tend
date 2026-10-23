package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SummaryInput includes complete ledger observations missing from native history.
// PreviousSummary compresses the unchanged prefix; observations replace items by
// message_id, with explicit tombstones for removals.
type SummaryInput struct {
	Snapshot        store.HandoffSnapshot
	PreviousSummary *store.HandoffSummary
	Observations    json.RawMessage
}

// HandoffSummaryInput captures coverage and all required input in one read.
// Budget exhaustion fails explicitly rather than claiming truncated coverage.
func (a *App) HandoffSummaryInput(ctx context.Context, instance string, maxBytes int) (out SummaryInput, err error) {
	if maxBytes <= 0 || maxBytes > 256<<10 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "summary input budget must be between 1 and 262144 bytes")
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out.Snapshot, err = store.GetHandoffSnapshot(ctx, tx.Q(), instance)
		if err != nil {
			return err
		}
		if out.Snapshot.Active || out.Snapshot.Pending || out.Snapshot.ThreadID == "" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "summary requires an idle bound conversation")
		}
		previous, err := store.GetHandoffSummary(ctx, tx.Q(), instance)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var since *int64
		if err == nil && previous.FormatVersion >= 2 && previous.Snapshot.Version <= out.Snapshot.Version {
			out.PreviousSummary = &previous
			since = &previous.Snapshot.Version
		}
		used := 0
		if out.PreviousSummary != nil {
			encoded, err := json.Marshal(out.PreviousSummary)
			if err != nil {
				return err
			}
			used = len(encoded)
		}
		if used+2 > maxBytes {
			return protocol.Errorf(protocol.CodeInvalidRequest, "complete summary input exceeds budget")
		}
		ids, err := store.SummaryObservationIDs(ctx, tx.Q(), since, maxBytes+1)
		if err != nil {
			return err
		}
		data := []byte{'['}
		for i, id := range ids {
			row, err := store.GetConversationItem(ctx, tx.Q(), id)
			var item any
			if errors.Is(err, store.ErrNotFound) {
				item = struct {
					MessageID string `json:"message_id"`
					Deleted   bool   `json:"deleted"`
				}{id, true}
			} else if err != nil {
				return err
			} else {
				it := protocol.ConversationItem{Seq: row.Seq, Kind: row.Kind, MessageID: row.MessageID, Author: row.Author, Body: row.Body, ReplyTo: row.ReplyTo, CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), AssistantState: protocol.AssistantState(row.AssistantState), AssistantRevision: row.AssistantRevision}
				if row.Tool != nil {
					it.Tool = &protocol.ToolUseView{Name: row.Tool.Tool, Input: row.Tool.Input, Output: row.Tool.Output, Status: protocol.ToolStatus(row.Tool.Status), TurnID: row.Tool.TurnID, UpdatedAt: time.UnixMilli(row.Tool.UpdatedAt).UTC()}
				} else if row.Author == "owner" {
					it.State = protocol.MessageState(row.State)
					if row.State == "" {
						it.State = protocol.MessageQueued
					}
				}
				if row.DecisionJSON != "" {
					var choice protocol.DecisionChoice
					if err := json.Unmarshal([]byte(row.DecisionJSON), &choice); err != nil {
						return err
					}
					it.Decision = &choice
				}
				it.Images, err = store.ConversationImages(ctx, tx.Q(), id)
				if err != nil {
					return err
				}
				item = it
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if i > 0 {
				data = append(data, ',')
			}
			if used+len(data)+len(encoded)+1 > maxBytes {
				return protocol.Errorf(protocol.CodeInvalidRequest, "complete summary input exceeds budget")
			}
			data = append(data, encoded...)
		}
		data = append(data, ']')
		out.Observations = data
		return nil
	})
	if err != nil {
		return SummaryInput{}, err
	}
	return out, nil
}
