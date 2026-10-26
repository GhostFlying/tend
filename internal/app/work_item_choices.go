package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type CreateWorkItemChoice struct{}

func (CreateWorkItemChoice) Operation() string { return "work_item_choices.create" }
func (CreateWorkItemChoice) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (CreateWorkItemChoice) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CreateWorkItemChoicePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := store.GetWorkItem(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "work item does not exist")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != w.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
	}
	return createChoice(ctx, x, tx, w, p, "")
}
func createChoice(ctx context.Context, x *Exec, tx *store.Tx, w protocol.WorkItem, p protocol.CreateWorkItemChoicePayload, supersedes string) (Outcome, error) {
	if w.Status == "closed" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "closed work item cannot ask a new question")
	}
	if strings.TrimSpace(p.Question) == "" || p.SourceID == "" || len(p.Options) > 10 || slices.Contains(p.Options, "") || len(p.Options) != len(dedupe(p.Options)) || p.Recommendation != "" && !slices.Contains(p.Options, p.Recommendation) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "question, source and distinct options with a valid recommendation are required")
	}
	var sourceValid bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND author='owner') OR EXISTS(SELECT 1 FROM work_item_completion_reviews WHERE work_item_id=? AND event_id=?)`, p.SourceID, w.WorkItemID, p.SourceID).Scan(&sourceValid); err != nil {
		return Outcome{}, err
	}
	if !sourceValid {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "source must be an owner message or this work item's completion event")
	}
	for _, id := range p.TaskIDs {
		var linked bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_task_links WHERE work_item_id=? AND task_id=?)`, w.WorkItemID, id).Scan(&linked); err != nil {
			return Outcome{}, err
		}
		if !linked {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "choice task must belong to work item")
		}
	}
	for _, id := range p.DeliveryIDs {
		if !slices.ContainsFunc(w.Deliveries, func(d protocol.WorkItemDelivery) bool { return d.DeliveryID == id }) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "choice delivery must belong to work item")
		}
	}
	key := p.SourceID
	if supersedes != "" {
		key = "replacement:" + supersedes
	}
	var existing string
	err := tx.Q().QueryRowContext(ctx, `SELECT choice_id FROM work_item_choices WHERE work_item_id=? AND source_key=?`, w.WorkItemID, key).Scan(&existing)
	if err == nil {
		c, e := store.GetWorkItemChoice(ctx, tx.Q(), existing)
		return Outcome{Result: c}, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	c := protocol.WorkItemChoice{ChoiceID: x.IDs.New(protocol.Prefix("choice")), WorkItemID: w.WorkItemID, Version: 1, Status: "open", Question: p.Question, Options: p.Options, Recommendation: p.Recommendation, SourceID: p.SourceID, TaskIDs: p.TaskIDs, DeliveryIDs: p.DeliveryIDs, Supersedes: supersedes, MessageID: x.IDs.New(protocol.PrefixMessage), CreatedAt: now, UpdatedAt: now}
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: c.MessageID, Author: "system", Body: p.Question, CreatedAt: now})
	if err != nil {
		return Outcome{}, err
	}
	if err := tx.BindMessage(ctx, c.MessageID, "work_item_choice", c.ChoiceID, "notice"); err != nil {
		return Outcome{}, err
	}
	if err := tx.BindMessage(ctx, c.MessageID, "work_item", w.WorkItemID, "notice"); err != nil {
		return Outcome{}, err
	}
	if err := tx.SaveWorkItemChoice(ctx, c, key, 0); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: c, Events: []Event{choiceEvent(c, "opened"), {ObjectType: "message", ObjectID: c.MessageID, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": "system"}}}}, nil
}

type UpdateWorkItemChoice struct {
	Action      string
	FromMessage bool
}

func (c UpdateWorkItemChoice) Operation() string {
	if c.FromMessage {
		return "work_item_choices.resolve_from_message"
	}
	return "work_item_choices." + c.Action
}
func (c UpdateWorkItemChoice) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if c.Action == "respond" && !c.FromMessage {
		return allowKinds(x.Principal, protocol.PrincipalOwner)
	}
	return allowKinds(x.Principal, planners...)
}
func (cmd UpdateWorkItemChoice) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.UpdateWorkItemChoicePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	c, err := store.GetWorkItemChoice(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "choice does not exist")
	}
	if c.Status != "open" || x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != c.Version {
		pe := protocol.Errorf(protocol.CodeStaleRevision, "choice is no longer open at this version")
		pe.Details, _ = json.Marshal(c)
		return Outcome{}, pe
	}
	old := c.Version
	c.Version++
	c.UpdatedAt = x.Now.UnixMilli()
	c.Reason = x.Env.Reason
	var sourceEvents []Event
	switch cmd.Action {
	case "respond":
		if strings.TrimSpace(p.Answer) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "answer is required")
		}
		if cmd.FromMessage && p.OwnerMessageID == "" && x.Principal.Addon() {
			// Native addon conversations are not Tend owner messages. Preserve
			// the relay as an agent-authored record with its audited request.
			id := x.IDs.New(protocol.PrefixMessage)
			seq, e := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "main_agent", Body: "转交 owner 的回答：\n\n" + p.Answer, ReplyTo: c.MessageID, RequestID: x.Env.RequestID, CreatedAt: c.UpdatedAt})
			if e != nil {
				return Outcome{}, e
			}
			for _, b := range []struct{ kind, id string }{{"work_item_choice", c.ChoiceID}, {"work_item", c.WorkItemID}} {
				if e := tx.BindMessage(ctx, id, b.kind, b.id, "answers"); e != nil {
					return Outcome{}, e
				}
			}
			c.AnswerSourceID = id
			c.AnswerSourceKind = "addon_relay"
			sourceEvents = append(sourceEvents, Event{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": "main_agent", "reply_to": c.MessageID}})
		} else if cmd.FromMessage {
			m, e := store.GetMessage(ctx, tx.Q(), p.OwnerMessageID)
			if e != nil {
				return Outcome{}, errNotFound(e, "owner message does not exist")
			}
			if m.Author != "owner" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "answer must cite an actual owner message")
			}
			c.AnswerSourceID = p.OwnerMessageID
			c.AnswerSourceKind = "owner_message"
		} else {
			id := x.IDs.New(protocol.PrefixMessage)
			seq, e := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "owner", Body: p.Answer, ReplyTo: c.MessageID, CreatedAt: c.UpdatedAt})
			if e != nil {
				return Outcome{}, e
			}
			c.AnswerSourceID = id
			c.AnswerSourceKind = "owner_message"
			if _, e = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"owner"}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"owner"}`, CreatedAt: c.UpdatedAt}); e != nil {
				return Outcome{}, e
			}
		}
		c.Status = "answered"
		c.Answer = p.Answer
	case "withdraw", "replace":
		if strings.TrimSpace(c.Reason) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "withdrawing or replacing a choice requires a reason")
		}
		c.Status = "withdrawn"
		if cmd.Action == "replace" {
			if p.Replacement == nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "replacement is required")
			}
			w, e := store.GetWorkItem(ctx, tx.Q(), c.WorkItemID)
			if e != nil {
				return Outcome{}, e
			}
			o, e := createChoice(ctx, x, tx, w, *p.Replacement, c.ChoiceID)
			if e != nil {
				return Outcome{}, e
			}
			next := o.Result.(protocol.WorkItemChoice)
			c.Status = "superseded"
			c.ReplacedBy = next.ChoiceID
			if e := tx.SaveWorkItemChoice(ctx, c, "", old); e != nil {
				return Outcome{}, e
			}
			o.Result = c
			o.Events = append(o.Events, choiceEvent(c, "superseded"))
			return o, nil
		}
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown choice operation")
	}
	if err := tx.SaveWorkItemChoice(ctx, c, "", old); err != nil {
		return Outcome{}, err
	}
	events := append(sourceEvents, choiceEvent(c, c.Status))
	if c.Status == "answered" && !cmd.FromMessage {
		// A recorded choice answer is already handled as input; its follow-up
		// coordination runs from a separate system notice, not this owner message.
		events = append(events, Event{ObjectType: "message", ObjectID: c.AnswerSourceID, Kind: "conversation.message_state", Body: map[string]any{"state": protocol.MessageReplied}})
	}
	return Outcome{Result: c, Events: events}, nil
}
func choiceEvent(c protocol.WorkItemChoice, state string) Event {
	return Event{ObjectType: "work_item_choice", ObjectID: c.ChoiceID, ObjectVersion: c.Version, Kind: "work_item_choice." + state, Body: map[string]any{"work_item_id": c.WorkItemID, "message_id": c.MessageID, "status": c.Status, "summary": fmt.Sprintf("%s: %s", state, c.Question)}}
}
