package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// readMessagingEvents projects a ledger batch without reading message bodies,
// attachments, or processing state. The batch cursor still follows the ledger.
func readMessagingEvents(ctx context.Context, q store.Querier, after int64) ([]store.EventView, map[int64]protocol.MessagingEvent, error) {
	batch, err := store.ListEvents(ctx, q, after, eventBatch)
	if err != nil {
		return nil, nil, err
	}

	var messages, tasks, runs, decisions, turns []string
	for _, ev := range batch {
		kind := messagingEventKind(ev.Kind)
		switch kind {
		case store.MessagingTurnProcessing:
			turns = append(turns, ev.ObjectID)
		case "messaging.input", "messaging.associations_changed":
			messages = append(messages, ev.ObjectID)
		case "messaging.processing":
			if ev.Kind != kind {
				messages = append(messages, ev.ObjectID)
			}
		case "messaging.work_changed":
			switch ev.ObjectType {
			case "task":
				tasks = append(tasks, ev.ObjectID)
			case "run":
				runs = append(runs, ev.ObjectID)
			case "decision":
				decisions = append(decisions, ev.ObjectID)
			}
		}
	}

	directions := map[string]string{}
	work := map[protocol.Target]string{}
	turnInputs := map[string][]string{}
	if len(messages)+len(tasks)+len(runs)+len(decisions)+len(turns) > 0 {
		if err := readMessagingEventScopes(ctx, q, messages, tasks, runs, decisions, turns, directions, work, turnInputs); err != nil {
			return nil, nil, err
		}
	}
	projected := make(map[int64]protocol.MessagingEvent)
	for _, ev := range batch {
		kind := messagingEventKind(ev.Kind)
		if kind == "" {
			continue
		}
		if kind != ev.Kind {
			switch kind {
			case "messaging.input", "messaging.processing":
				if directions[ev.ObjectID] != "input" {
					continue
				}
			case "messaging.associations_changed":
				if directions[ev.ObjectID] == "" {
					continue
				}
			}
		}
		event := protocol.MessagingEvent{EventSeq: ev.Seq, Kind: kind, ObjectType: ev.ObjectType, ObjectID: ev.ObjectID}
		if kind == store.MessagingTurnProcessing {
			event.MessageIDs = turnInputs[ev.ObjectID]
		}
		if kind == "messaging.work_changed" {
			switch ev.ObjectType {
			case "issue", "work_item":
				event.IssueID = ev.ObjectID
			case "task", "run", "decision":
				event.IssueID = work[protocol.Target{Type: ev.ObjectType, ID: ev.ObjectID}]
			}
		}
		projected[ev.Seq] = event
	}
	return batch, projected, nil
}

func messagingEventKind(kind string) string {
	switch {
	case kind == "conversation.message":
		return "messaging.input"
	case kind == "conversation.associations_changed":
		return "messaging.associations_changed"
	case kind == "conversation.message_state", kind == "conversation.urgent_consumed", kind == "conversation.turn_reviewed",
		strings.HasPrefix(kind, "conversation.processing_"), strings.HasPrefix(kind, "conversation.clarification_"):
		return "messaging.processing"
	case strings.HasPrefix(kind, "task."), strings.HasPrefix(kind, "issue."), strings.HasPrefix(kind, "decision."), kind == "run.observed":
		return "messaging.work_changed"
	case strings.HasPrefix(kind, "messaging."):
		return kind
	default:
		return ""
	}
}

func readMessagingEventScopes(ctx context.Context, q store.Querier, messages, tasks, runs, decisions, turns []string, directions map[string]string, work map[protocol.Target]string, turnInputs map[string][]string) error {
	messageIDs, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	taskIDs, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	runIDs, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	decisionIDs, err := json.Marshal(decisions)
	if err != nil {
		return err
	}
	turnIDs, err := json.Marshal(turns)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT 'message',message_id,direction FROM messaging_messages WHERE message_id IN (SELECT value FROM json_each(?))
		UNION ALL SELECT 'task',task_id,work_item_id FROM task_work_item_owners WHERE task_id IN (SELECT value FROM json_each(?))
		UNION ALL SELECT 'run',run_id,work_item_id FROM runs JOIN task_work_item_owners USING(task_id)
			WHERE run_id IN (SELECT value FROM json_each(?))
		UNION ALL SELECT 'decision',decision_id,work_item_id FROM decisions JOIN task_work_item_owners USING(task_id)
			WHERE decision_id IN (SELECT value FROM json_each(?))
		UNION ALL SELECT 'logical_turn',l.logical_turn_id,l.message_id
			FROM logical_turn_messages l JOIN messaging_messages m USING(message_id)
			WHERE l.logical_turn_id IN (SELECT value FROM json_each(?)) AND m.direction='input'`,
		string(messageIDs), string(taskIDs), string(runIDs), string(decisionIDs), string(turnIDs))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var target protocol.Target
		var value string
		if err := rows.Scan(&target.Type, &target.ID, &value); err != nil {
			return err
		}
		switch target.Type {
		case "message":
			directions[target.ID] = value
		case "logical_turn":
			turnInputs[target.ID] = append(turnInputs[target.ID], value)
		default:
			work[target] = value
		}
	}
	return rows.Err()
}
