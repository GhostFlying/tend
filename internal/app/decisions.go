package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// OpenDecision handles decisions.open: a planner asks the owner a question,
// optionally about a task. Asking does not block the task; follow-up work
// waits only where the planner makes it wait.
type OpenDecision struct{}

type openDecisionPayload struct {
	TaskID   string   `json:"task_id,omitempty"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	// SourceMessageID is the conversation message that asks the question.
	SourceMessageID string `json:"source_message_id,omitempty"`
}

// Operation implements Command.
func (OpenDecision) Operation() string { return "decisions.open" }

// Authorize implements Command.
func (OpenDecision) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}

// Apply implements Command.
func (OpenDecision) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p openDecisionPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Question == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "question is required")
	}
	if len(p.Options) > 10 || slices.Contains(p.Options, "") || len(p.Options) != len(dedupe(p.Options)) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "options must be at most 10 distinct non-empty strings")
	}
	if p.TaskID != "" {
		if _, err := store.GetTask(ctx, tx.Q(), p.TaskID); err != nil {
			return Outcome{}, errNotFound(err, "task %s does not exist", p.TaskID)
		}
	}
	options, err := json.Marshal(p.Options)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: encode options: %w", err)
	}
	id := x.IDs.New(protocol.PrefixDecision)
	if err := tx.InsertDecision(ctx, id, p.TaskID, p.Question, string(options), x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if p.SourceMessageID != "" {
		if err := tx.SetDecisionAskedMessage(ctx, id, p.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "decision", id); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result: map[string]any{"decision_id": id, "version": 1},
		Events: []Event{{ObjectType: "decision", ObjectID: id, ObjectVersion: 1, Kind: "decision.opened", Body: map[string]any{"task_id": p.TaskID}}},
	}, nil
}

func dedupe(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

// AnswerDecision handles decisions.respond, the owner answering from a
// client, and decisions.resolve_from_message, the main agent recording the
// answer the owner gave in the conversation. The answer only closes the
// decision: whatever it asks for, such as a retry, is a separate command.
//
// The answer is checked against the version the answerer saw. Answering an
// answered decision again with the same answer returns the recorded result;
// a different answer gets stale_revision and the current state.
type AnswerDecision struct {
	// FromMessage selects decisions.resolve_from_message.
	FromMessage bool
}

type decisionAnswer struct {
	Option string `json:"option,omitempty"`
	Text   string `json:"text,omitempty"`
}

// Operation implements Command.
func (c AnswerDecision) Operation() string {
	if c.FromMessage {
		return "decisions.resolve_from_message"
	}
	return "decisions.respond"
}

// Authorize implements Command.
func (c AnswerDecision) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if c.FromMessage {
		return allowKinds(x.Principal, protocol.PrincipalMainAgent, protocol.PrincipalSystem)
	}
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

// Apply implements Command.
func (c AnswerDecision) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.AnswerDecisionPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Option == "" && p.Text == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "option or text is required")
	}
	if c.FromMessage && p.SourceMessageID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "source_message_id is required")
	}
	d, err := store.GetDecision(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "decision %s does not exist", x.Env.Target.ID)
	}
	var options []string
	if err := json.Unmarshal([]byte(d.OptionsJSON), &options); err != nil {
		return Outcome{}, fmt.Errorf("app: decode options of %s: %w", d.DecisionID, err)
	}
	if p.Option != "" && !slices.Contains(options, p.Option) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "%q is not an option of this decision", p.Option)
	}
	answer, err := json.Marshal(decisionAnswer{Option: p.Option, Text: p.Text})
	if err != nil {
		return Outcome{}, fmt.Errorf("app: encode answer: %w", err)
	}
	result := func(version int64) map[string]any {
		return map[string]any{"decision_id": d.DecisionID, "status": store.DecisionAnswered, "version": version, "answer": json.RawMessage(answer)}
	}
	if d.Status == store.DecisionAnswered && d.AnswerJSON == string(answer) {
		return Outcome{Result: result(d.Version)}, nil
	}
	if d.Status != store.DecisionOpen || d.Version != p.Version {
		pe := protocol.Errorf(protocol.CodeStaleRevision, "the decision changed since version %d", p.Version)
		if b, err := json.Marshal(map[string]any{"version": d.Version, "status": d.Status, "answer": json.RawMessage(nonEmpty(d.AnswerJSON))}); err == nil {
			pe.Details = b
		}
		return Outcome{}, pe
	}
	if err := tx.AnswerDecision(ctx, d.DecisionID, d.Version, string(answer), p.SourceMessageID, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "decision", d.DecisionID); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result: result(d.Version + 1),
		Events: []Event{{ObjectType: "decision", ObjectID: d.DecisionID, ObjectVersion: d.Version + 1, Kind: "decision.answered",
			Body: map[string]any{"task_id": d.TaskID, "answer": json.RawMessage(answer)}}},
	}, nil
}

func nonEmpty(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
