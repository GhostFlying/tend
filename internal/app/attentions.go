package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AttentionCommand changes a person's obligation, never the Issue's assignee.
type AttentionCommand struct {
	Action      string
	FromMessage bool
}

func (c AttentionCommand) Operation() string { return "attentions." + c.Action }
func (AttentionCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c AttentionCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "attention" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "attention target is required")
	}
	if c.Action == "create" {
		return createAttention(ctx, x, tx)
	}
	a, err := store.GetAttention(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "attention does not exist")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if a.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "attention changed")
	}
	old := a.Revision
	switch c.Action {
	case "present":
		var p protocol.PresentAttentionPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.Read != nil {
			a.ReadAt = 0
			if *p.Read {
				a.ReadAt = x.Now.UnixMilli()
			}
		}
		if p.Archived != nil {
			a.ArchivedAt = 0
			if *p.Archived {
				a.ArchivedAt = x.Now.UnixMilli()
			}
		}
	case "update":
		var p protocol.UpdateAttentionPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if a.Status != "open" || strings.TrimSpace(p.Title) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only an open attention can be revised")
		}
		previousClock := a
		if err := setAttentionActionTime(&a, p.LatestActionAt, p.LatestActionTimezone, p.LatestActionReminders); err != nil {
			return Outcome{}, err
		}
		if p.Title == a.Title && p.BlocksClose == a.BlocksClose && (p.Options == nil || slices.Equal(p.Options, a.QuestionOptions)) && attentionActionTimeEqual(previousClock, a) && previousClock.LatestActionTimezone == a.LatestActionTimezone {
			return Outcome{Result: a}, nil
		}
		if a.Kind == "decision" && (p.Title != a.Title || p.Options != nil) {
			if a.DecisionID == "" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "legacy choice revisions use their original business path")
			}
			d, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
			if err != nil {
				return Outcome{}, err
			}
			if d.TaskID != "" || d.Worker != nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "task and worker questions retain their original frozen question")
			}
			if d.Version != p.DecisionVersion {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original decision changed")
			}
			options := p.Options
			if options == nil {
				if err := json.Unmarshal([]byte(d.OptionsJSON), &options); err != nil {
					return Outcome{}, err
				}
			}
			if len(options) > 10 || len(dedupe(options)) != len(options) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "distinct bounded options required")
			}
			for _, option := range options {
				if strings.TrimSpace(option) == "" {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "nonempty options required")
				}
			}
			raw, _ := json.Marshal(options)
			if _, err := tx.Exec(ctx, `UPDATE decisions SET question=?,options_json=?,version=version+1,updated_at=? WHERE decision_id=? AND version=? AND status='open'`, p.Title, string(raw), x.Now.UnixMilli(), d.DecisionID, d.Version); err != nil {
				return Outcome{}, err
			}
		}
		if a.Kind == "information" && p.BlocksClose {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "information does not block Issue closure")
		}
		a.Title, a.BlocksClose = p.Title, p.BlocksClose
	case "respond":
		if a.Kind != "decision" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "answer the original decision")
		}
		var p protocol.RespondAttentionPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, a.IssueID, p.SourceMessageID); err != nil {
			return Outcome{}, err
		}
		inner := *x
		env := *x.Env
		inner.Env = &env
		if a.DecisionID != "" {
			env.Operation = "decisions.respond"
			env.Target = protocol.Target{Type: "decision", ID: a.DecisionID}
			from := x.Principal.Kind != protocol.PrincipalOwner
			if from {
				env.Operation = "decisions.resolve_from_message"
			}
			env.Payload, _ = json.Marshal(protocol.AnswerDecisionPayload(p))
			out, e := (AnswerDecision{FromMessage: from}).Apply(ctx, &inner, tx)
			if e != nil {
				return Outcome{}, e
			}
			d, e := store.GetDecision(ctx, tx.Q(), a.DecisionID)
			if e != nil {
				return Outcome{}, e
			}
			if len(out.Events) == 0 && a.Status == "resolved" {
				out.Result = a
				return out, nil
			}
			if d.Status == store.DecisionAnswered {
				a.Status = "resolved"
			}
			if err := saveAttention(ctx, x, tx, &a, old); err != nil {
				return Outcome{}, err
			}
			out.Result = a
			return out, nil
		}
		if a.ChoiceID != "" {
			env.Target = protocol.Target{Type: "work_item_choice", ID: a.ChoiceID}
			env.ExpectedControlRevision = &p.Version
			env.Payload, _ = json.Marshal(protocol.UpdateWorkItemChoicePayload{Answer: p.Text, OwnerMessageID: p.SourceMessageID})
			out, e := (UpdateWorkItemChoice{Action: "respond", FromMessage: x.Principal.Kind != protocol.PrincipalOwner}).Apply(ctx, &inner, tx)
			if e != nil {
				return Outcome{}, e
			}
			a.Status = "resolved"
			if err := saveAttention(ctx, x, tx, &a, old); err != nil {
				return Outcome{}, err
			}
			out.Result = a
			return out, nil
		}
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "attention has no original question")
	case "confirm":
		if a.Kind != "action" || a.Status != "open" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only an open human action can be confirmed")
		}
		var p protocol.ConfirmAttentionPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if strings.TrimSpace(p.Summary) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit completion summary is required")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, a.IssueID, p.SourceMessageID); err != nil {
			return Outcome{}, err
		}
		a.Status, a.Resolution, a.ResolutionSourceID = "resolved", p.Summary, p.SourceMessageID
	case "withdraw":
		if a.Kind == "decision" {
			if a.DecisionID == "" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "withdraw legacy choices through their original business path")
			}
			d, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
			if err != nil {
				return Outcome{}, err
			}
			if d.Worker != nil || d.TaskID != "" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "task and worker questions keep their original run/control cancellation path")
			}
			if d.Status != "open" {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "decision is no longer open")
			}
			if _, err := tx.Exec(ctx, `UPDATE decisions SET status='cancelled',version=version+1,updated_at=? WHERE decision_id=? AND version=? AND status='open'`, x.Now.UnixMilli(), d.DecisionID, d.Version); err != nil {
				return Outcome{}, err
			}
		}
		if a.Status != "open" || strings.TrimSpace(x.Env.Reason) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "withdraw an open attention with a reason")
		}
		a.Status, a.Resolution = "withdrawn", x.Env.Reason
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported attention operation")
	}
	if err := saveAttention(ctx, x, tx, &a, old); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Result: a}
	if c.Action != "present" {
		out.Events = []Event{attentionEvent(a)}
	}
	return out, nil
}
func saveAttention(ctx context.Context, x *Exec, tx *store.Tx, a *protocol.Attention, old int64) error {
	if a.DecisionID != "" {
		d, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
		if err != nil {
			return err
		}
		a.QuestionVersion = d.Version
		if err := json.Unmarshal([]byte(d.OptionsJSON), &a.QuestionOptions); err != nil {
			return err
		}
	}
	var previous protocol.Attention
	if old > 0 {
		var err error
		previous, err = store.GetAttention(ctx, tx.Q(), a.AttentionID)
		if err != nil {
			return err
		}
	}
	if err := syncAttentionActionClock(ctx, x, tx, a, previous); err != nil {
		return err
	}
	a.Revision = old + 1
	a.UpdatedAt = x.Now.UnixMilli()
	return tx.SaveAttention(ctx, *a, old, x.Env.RequestID)
}
func attentionEvent(a protocol.Attention) Event {
	return Event{ObjectType: "attention", ObjectID: a.AttentionID, ObjectVersion: a.Revision, Kind: "attention.changed", Body: map[string]any{"issue_id": a.IssueID, "status": a.Status}}
}
func createAttention(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.ID != "" || x.Env.ExpectedControlRevision != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "a new attention has no current identity or revision")
	}
	var p protocol.CreateAttentionPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !oneOf(p.Kind, "decision", "action", "information") || strings.TrimSpace(p.Title) == "" || p.Kind == "information" && p.BlocksClose {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "attention kind/title and nonblocking information are required")
	}
	if p.IssueID != "" {
		issue, e := store.GetCanonicalIssue(ctx, tx.Q(), p.IssueID)
		if e != nil {
			return Outcome{}, e
		}
		if issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "terminal Issue cannot raise a new obligation")
		}
	}
	if p.TaskID != "" {
		primary, e := store.GetTaskIssueOwner(ctx, tx.Q(), p.TaskID)
		if e != nil {
			return Outcome{}, e
		}
		if p.IssueID == "" || primary != p.IssueID {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "attention task must have this primary Issue")
		}
	}
	if p.SourceMessageID != "" {
		if _, e := store.GetMessage(ctx, tx.Q(), p.SourceMessageID); e != nil {
			return Outcome{}, e
		}
	}
	if p.DecisionID != "" && p.ChoiceID != "" || p.Kind != "decision" && (p.DecisionID != "" || p.ChoiceID != "") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "only a decision attention references one question")
	}
	a := protocol.Attention{AttentionID: x.IDs.New(protocol.Prefix("attn")), IssueID: p.IssueID, TaskID: p.TaskID, Kind: p.Kind, Title: p.Title, Status: "open", DecisionID: p.DecisionID, ChoiceID: p.ChoiceID, SourceMessageID: p.SourceMessageID, RaisedBy: x.Principal, BlocksClose: p.BlocksClose, CreatedAt: x.Now.UnixMilli()}
	if err := setAttentionActionTime(&a, p.LatestActionAt, p.LatestActionTimezone, p.LatestActionReminders); err != nil {
		return Outcome{}, err
	}
	var events []Event
	if a.Kind == "decision" {
		if a.DecisionID == "" && a.ChoiceID == "" {
			inner := *x
			env := *x.Env
			inner.Env = &env
			env.Payload, _ = json.Marshal(openDecisionPayload{TaskID: p.TaskID, Question: p.Title, Options: p.Options, SourceMessageID: p.SourceMessageID})
			out, e := (OpenDecision{}).Apply(ctx, &inner, tx)
			if e != nil {
				return Outcome{}, e
			}
			a.DecisionID = out.Result.(map[string]any)["decision_id"].(string)
			events = out.Events
		} else {
			var existing string
			err := tx.Q().QueryRowContext(ctx, `SELECT attention_id FROM attentions WHERE decision_id=NULLIF(?,'') OR choice_id=NULLIF(?,'')`, a.DecisionID, a.ChoiceID).Scan(&existing)
			if err == nil {
				old, e := store.GetAttention(ctx, tx.Q(), existing)
				if e == nil && x.Principal.Kind == protocol.PrincipalBackgroundAgent && old.IssueID != p.IssueID {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "question belongs to another responsibility")
				}
				if e == nil && (p.LatestActionAt != nil || p.LatestActionTimezone != "" || p.LatestActionReminders != nil) {
					requested := old
					if err := setAttentionActionTime(&requested, p.LatestActionAt, p.LatestActionTimezone, p.LatestActionReminders); err != nil {
						return Outcome{}, err
					}
					if !attentionActionTimeEqual(old, requested) || old.LatestActionTimezone != requested.LatestActionTimezone {
						return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "update the existing Attention action time with its current revision")
					}
				}
				return Outcome{Result: old}, e
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return Outcome{}, err
			}
			if a.DecisionID != "" {
				d, e := store.GetDecision(ctx, tx.Q(), a.DecisionID)
				if e != nil {
					return Outcome{}, e
				}
				if d.TaskID != p.TaskID {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "decision task does not match")
				}
				if x.Principal.Kind == protocol.PrincipalBackgroundAgent && d.TaskID == "" {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "existing taskless decision has no primary scope for this manager")
				}
				a.Title = d.Question
				if d.Status != store.DecisionOpen {
					a.Status = "resolved"
					if d.Status != store.DecisionAnswered {
						a.Status = "withdrawn"
					}
				}
			} else {
				d, e := store.GetWorkItemChoice(ctx, tx.Q(), a.ChoiceID)
				if e != nil {
					return Outcome{}, e
				}
				if p.IssueID != "" && d.WorkItemID != p.IssueID {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "choice belongs to another Issue")
				}
				a.Title = d.Question
				if d.Status != "open" {
					a.Status = "resolved"
					if d.Status != "answered" {
						a.Status = "withdrawn"
					}
				}
			}
		}
	}
	if err := saveAttention(ctx, x, tx, &a, 0); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: a, Events: append(events, attentionEvent(a))}, nil
}
func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// syncAttentionDecision is driven by the original question's durable event.
func syncAttentionDecision(ctx context.Context, x *Exec, tx *store.Tx, e Event) (string, error) {
	column := "decision_id"
	if e.ObjectType == "work_item_choice" {
		column = "choice_id"
	}
	var id string
	err := tx.Q().QueryRowContext(ctx, `SELECT attention_id FROM attentions WHERE `+column+`=?`, e.ObjectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if e.ObjectType != "decision" || e.Kind != "decision.opened" {
			return "", nil
		}
		d, err := store.GetDecision(ctx, tx.Q(), e.ObjectID)
		if err != nil {
			return "", err
		}
		issue := ""
		if d.TaskID != "" {
			issue, err = store.GetTaskIssueOwner(ctx, tx.Q(), d.TaskID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return "", err
			}
			if issue != "" {
				var canonical bool
				if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, issue).Scan(&canonical); err != nil {
					return "", err
				}
				if !canonical {
					issue = ""
				}
			}
		}
		a := protocol.Attention{AttentionID: x.IDs.New(protocol.Prefix("attn")), IssueID: issue, TaskID: d.TaskID, Kind: "decision", Title: d.Question, Status: "open", DecisionID: d.DecisionID, SourceMessageID: d.AskedMessageID, RaisedBy: x.Principal, CreatedAt: x.Now.UnixMilli()}
		if err := saveAttention(ctx, x, tx, &a, 0); err != nil {
			return "", err
		}
		return a.IssueID, nil
	}
	if err != nil {
		return "", err
	}
	a, err := store.GetAttention(ctx, tx.Q(), id)
	if err != nil {
		return "", err
	}
	state := "open"
	var original string
	if e.ObjectType == "decision" {
		d, err := store.GetDecision(ctx, tx.Q(), e.ObjectID)
		if err != nil {
			return "", err
		}
		original = d.Status
	} else {
		d, err := store.GetWorkItemChoice(ctx, tx.Q(), e.ObjectID)
		if err != nil {
			return "", err
		}
		original = d.Status
	}
	if original == "answered" {
		state = "resolved"
	} else if original != "open" {
		state = "withdrawn"
	}
	if a.Status != state {
		a.Status = state
		if err := saveAttention(ctx, x, tx, &a, a.Revision); err != nil {
			return "", err
		}
	}
	return a.IssueID, nil
}

// Owners act directly. Agent relays use an admitted, unwithdrawn owner original; only
// local managers also require a source selected for their own Issue.
func validateAttentionOwnerRelay(ctx context.Context, q store.Querier, x *Exec, issue, message string) error {
	if x.Principal.Kind == protocol.PrincipalOwner {
		return nil
	}
	var original bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.message_id=? AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0)`, message).Scan(&original); err != nil {
		return err
	}
	if !original {
		return protocol.Errorf(protocol.CodeForbidden, "relay an actual owner source that has not been withdrawn")
	}
	if err := requireHostedSourceAdmitted(ctx, q, message); err != nil {
		return err
	}
	if x.Principal.Kind != protocol.PrincipalBackgroundAgent {
		return nil
	}
	var allowed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_work_items a JOIN conversation_messages m USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE a.message_id=? AND a.work_item_id=? AND a.active=1 AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0)`, message, issue).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return protocol.Errorf(protocol.CodeForbidden, "relay only an actual owner source selected for this Issue")
	}
	return nil
}
