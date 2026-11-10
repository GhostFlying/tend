package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/domain"
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
	if err := tx.InsertDecision(ctx, id, p.TaskID, p.Question, string(options), "{}", nil, x.Now.UnixMilli()); err != nil {
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
// client, and decisions.resolve_from_message, a main agent recording the
// answer the owner gave in the conversation. When the chosen option of a
// decision tend opened has an effect, such as a retry, tend carries it out
// in the same transaction (D53); otherwise the answer only closes the
// decision and whatever it asks for is a separate command.
//
// The answer is checked against the version the answerer saw. Answering an
// answered decision again with the same answer returns the recorded result;
// a different answer gets stale_revision and the current state.
type AnswerDecision struct {
	// TypedContinuation selects explicit scheduling policy instead of the legacy answer contract.
	TypedContinuation bool
	// FromMessage selects decisions.resolve_from_message.
	FromMessage bool
	// Responsible is an internal path after the current supervisor was verified.
	Responsible bool
}

type decisionAnswer struct {
	Continuation *protocol.DecisionContinuationPolicy `json:"continuation,omitempty"`
	Option       string                               `json:"option,omitempty"`
	Text         string                               `json:"text,omitempty"`
	Effect       *protocol.EffectResult               `json:"effect,omitempty"`
	Actor        *protocol.Principal                  `json:"actor,omitempty"`
}

// Operation implements Command.
func (c AnswerDecision) Operation() string {
	if c.TypedContinuation {
		if c.FromMessage {
			return protocol.OpResolveDecisionContinuation
		}
		return protocol.OpAnswerDecisionContinuation
	}
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
	if c.TypedContinuation && (p.Continuation == nil || p.Version < 1 || x.Env.Target.Type != "decision") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "typed continuation requires a decision target, original version and explicit policy")
	}
	if !c.TypedContinuation && p.Continuation != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "use the explicit typed continuation operation")
	}
	if p.Option == "" && p.Text == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "option or text is required")
	}
	// The hosted main agent records answers from the conversation it
	// hosts. An agent the owner works in has no message to point to.
	if c.FromMessage && p.SourceMessageID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "source_message_id is required")
	}
	d, err := store.GetDecision(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "decision %s does not exist", x.Env.Target.ID)
	}
	if c.TypedContinuation && x.Env.Operation == c.Operation() {
		if d.Worker == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "typed continuation requires a worker handoff question")
		}
		expected, err := requireRevision(x.Env)
		if err != nil {
			return Outcome{}, err
		}
		task, err := store.GetTask(ctx, tx.Q(), d.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if expected != task.ControlRevision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "parent task control revision changed")
		}
		if c.FromMessage && !c.Responsible && x.coordinationExecution != nil && x.coordinationExecution.Selection.Purpose == agentconfig.IssueManager {
			if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, x.coordinationExecution.IssueID, p.SourceMessageID); err != nil {
				return Outcome{}, err
			}
		}
	}
	mixedExecutionChoice := p.Text != "" && oneOf(p.Option, "approve", "end_delivery")
	var delivery bool
	if c.Responsible || mixedExecutionChoice {
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_delivery_duties duty JOIN attentions a ON a.attention_id=json_extract(duty.body_json,'$.attention_id') WHERE a.decision_id=?)`, d.DecisionID).Scan(&delivery); err != nil {
			return Outcome{}, err
		}
		if delivery && c.Responsible {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "delivery confirmation requires an actual owner source; escalate the original question or relay its standing owner authorization")
		}
	}
	var options []string
	if err := json.Unmarshal([]byte(d.OptionsJSON), &options); err != nil {
		return Outcome{}, fmt.Errorf("app: decode options of %s: %w", d.DecisionID, err)
	}
	if p.Option != "" && !slices.Contains(options, p.Option) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "%q is not an option of this decision", p.Option)
	}
	if d.Status == store.DecisionAnswered {
		var prev decisionAnswer
		if err := json.Unmarshal([]byte(d.AnswerJSON), &prev); err == nil && prev.Option == p.Option && prev.Text == p.Text {
			if p.Continuation != nil && (prev.Continuation == nil || !reflect.DeepEqual(decisionContinuationPolicy(p.Continuation), *prev.Continuation)) {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "recorded continuation policy is immutable; use its independent resume operation")
			}
			return Outcome{Result: answerResult(d, d.Version, []byte(d.AnswerJSON), prev.Effect)}, nil
		}
	}
	if d.Status != store.DecisionOpen || d.Version != p.Version {
		pe := protocol.Errorf(protocol.CodeStaleRevision, "the decision changed since version %d", p.Version)
		if b, err := json.Marshal(map[string]any{"version": d.Version, "status": d.Status, "answer": json.RawMessage(nonEmpty(d.AnswerJSON))}); err == nil {
			pe.Details = b
		}
		return Outcome{}, pe
	}
	if delivery && mixedExecutionChoice {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery approve/end_delivery must omit text; use text-only for conditional answers or Envelope.reason for audit explanation; defer may include text")
	}
	if obsolete, err := store.TaskRuleQuestionObsolete(ctx, tx.Q(), d.DecisionID); err != nil {
		return Outcome{}, err
	} else if obsolete {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "the question belongs to a superseded task spec or action")
	}
	// Task-rule questions cannot accept new answers after that responsibility
	// ends. Previously accepted answers have already returned their stored fact.
	// A control revision alone is insufficient: pausing retains the question.
	if d.TaskID != "" && d.AskedRevision.Valid {
		task, err := store.GetTask(ctx, tx.Q(), d.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if task.Lifecycle.Terminal() {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "the question's task no longer applies")
		}
	}
	newOwnerSource := x.Principal.Kind == protocol.PrincipalOwner && p.SourceMessageID == ""
	var sourceEvents []Event
	if c.Responsible {
		if x.coordinationExecution == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "admitted supervisor execution is required")
		}
		message := x.IDs.New(protocol.PrefixMessage)
		conversation := x.coordinationExecution.ConversationID
		_, err = tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: conversation, Author: "agent", Body: strings.TrimSpace(p.Option + "\n" + p.Text), CreatedAt: x.Now.UnixMilli(), RequestID: x.Env.RequestID})
		p.SourceMessageID = message
	} else {
		sourceEvents, err = recordDecisionAnswerSource(ctx, x, tx, d, &p)
	}
	if err != nil {
		return Outcome{}, err
	}
	if p.SourceMessageID != "" {
		if d.TaskID != "" {
			if err := tx.BindMessage(ctx, p.SourceMessageID, "task", d.TaskID, "answers"); err != nil {
				return Outcome{}, err
			}
		}
		if !c.Responsible {
			if err := attachReviewSource(ctx, x, tx, p.SourceMessageID); err != nil {
				return Outcome{}, err
			}
		}
	}
	if err := recordDecisionContinuation(ctx, x, tx, d, p.Continuation); err != nil {
		return Outcome{}, err
	}
	effect, effectEvents, err := answerEffect(ctx, x, tx, d, p.Option)
	if err != nil {
		return Outcome{}, err
	}
	if newOwnerSource && p.Text == "" && p.Option != "" && effect != nil && effect.Applied {
		if err := reviewAppliedOwnerChoice(ctx, x, tx, d, p, *effect); err != nil {
			return Outcome{}, err
		}
		sourceEvents = append(sourceEvents, Event{ObjectType: "message", ObjectID: p.SourceMessageID, Kind: "conversation.turn_reviewed"})
	}
	value := decisionAnswer{Option: p.Option, Text: p.Text, Effect: effect, Continuation: p.Continuation}
	if value.Continuation != nil {
		policy := decisionContinuationPolicy(value.Continuation)
		value.Continuation = &policy
	}
	if c.Responsible {
		actor := x.ActualPrincipal()
		value.Actor = &actor
	}
	answer, err := json.Marshal(value)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: encode answer: %w", err)
	}
	if err := tx.AnswerDecision(ctx, d.DecisionID, d.Version, string(answer), p.SourceMessageID, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if p.Continuation != nil && p.Continuation.Mode == protocol.DecisionDependency {
		if err := validateCanonicalTaskDependencies(ctx, tx.Q()); err != nil {
			return Outcome{}, err
		}
	}
	if err := resumeAnsweredQuestion(ctx, x, tx, d.DecisionID); err != nil {
		return Outcome{}, err
	}

	if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_answers(decision_id,message_id,content_revision)
 SELECT ?,m.message_id,COALESCE(md.content_revision,1) FROM conversation_messages m LEFT JOIN message_dispositions md USING(message_id)
 WHERE m.message_id=? AND m.author='owner' AND EXISTS(SELECT 1 FROM issue_delivery_duties duty JOIN attentions a ON a.attention_id=json_extract(duty.body_json,'$.attention_id') WHERE a.decision_id=?)
 ON CONFLICT DO NOTHING`, d.DecisionID, p.SourceMessageID, d.DecisionID); err != nil {
		return Outcome{}, err
	}
	if p.Option == "defer" {
		rows, err := tx.Q().QueryContext(ctx, `UPDATE issue_delivery_duties SET status='deferred',revision=revision+1 WHERE status='awaiting_owner' AND json_extract(body_json,'$.attention_id') IN (SELECT attention_id FROM attentions WHERE decision_id=?) RETURNING issue_id,task_id,revision,status`, d.DecisionID)
		if err != nil {
			return Outcome{}, err
		}
		for rows.Next() {
			var issue string
			var duty protocol.IssueDeliveryDuty
			if err := rows.Scan(&issue, &duty.TaskID, &duty.Revision, &duty.Status); err != nil {
				rows.Close()
				return Outcome{}, err
			}
			sourceEvents = append(sourceEvents, deliveryDutyContextEvent(issue, duty))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Outcome{}, err
		}
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "decision", d.DecisionID); err != nil {
		return Outcome{}, err
	}
	events := append([]Event{{ObjectType: "decision", ObjectID: d.DecisionID, ObjectVersion: d.Version + 1, Kind: "decision.answered",
		Body: map[string]any{"task_id": d.TaskID, "answer": json.RawMessage(answer)}}}, effectEvents...)
	return Outcome{Result: answerResult(d, d.Version+1, answer, effect), Events: append(sourceEvents, events...)}, nil
}

func answerResult(d store.DecisionRow, version int64, answer []byte, effect *protocol.EffectResult) protocol.AnswerResult {
	return protocol.AnswerResult{DecisionID: d.DecisionID, Status: store.DecisionAnswered, Version: version, Answer: answer, Effect: effect}
}

// answerEffect carries out the effect of the chosen option, if it has one,
// through the same rules as the explicit command. The effect applies only
// to the task as it was when tend asked: once anyone changed its control
// state, the answer is kept as a fact and the effect is not applied, and
// neither is one the rules refuse. Either way the result says why.
func answerEffect(ctx context.Context, x *Exec, tx *store.Tx, d store.DecisionRow, option string) (*protocol.EffectResult, []Event, error) {
	var effects map[string]protocol.DecisionEffect
	if err := json.Unmarshal([]byte(d.EffectsJSON), &effects); err != nil {
		return nil, nil, fmt.Errorf("app: decode effects of %s: %w", d.DecisionID, err)
	}
	kind, ok := effects[option]
	if !ok || d.TaskID == "" || !d.AskedRevision.Valid {
		return nil, nil, nil
	}
	res := &protocol.EffectResult{Effect: kind}
	row, spec, err := loadTask(ctx, tx.Q(), d.TaskID)
	if err != nil {
		return nil, nil, err
	}
	if row.ControlRevision != d.AskedRevision.Int64 {
		res.Reason = fmt.Sprintf("the task changed after the question was asked (control revision %d, now %d)", d.AskedRevision.Int64, row.ControlRevision)
		return res, nil, nil
	}
	if kind != protocol.EffectCancel {
		currentOwner, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), row.TaskID)
		if err != nil {
			return nil, nil, err
		}
		if !currentOwner {
			res.Reason = "Issue responsibility changed; the answer is retained but a current owner execution contract is required"
			return res, nil, nil
		}
	}
	task := toDomainWithWorkspace(row, spec)
	var dec domain.Decision
	switch kind {
	case protocol.EffectImplement:
		dec, err = domain.RequestImplement(task, task.ImplementGeneration+1, row.ControlRevision)
	case protocol.EffectAccept:
		var latest int64
		if latest, err = store.LatestAcceptGeneration(ctx, tx.Q(), row.TaskID, row.SpecVersion, row.CurrentArtifactID); err != nil {
			return nil, nil, err
		}
		dec, err = domain.RequestAcceptance(task, latest, latest+1, row.ControlRevision)
	case protocol.EffectCancel:
		dec, err = domain.Cancel(task, row.ControlRevision)
	case protocol.EffectStartOver:
		dec, err = domain.StartOver(task, row.ControlRevision)
	default:
		res.Reason = fmt.Sprintf("unknown effect %q", kind)
		return res, nil, nil
	}
	if pe := (*protocol.Error)(nil); errors.As(err, &pe) {
		res.Reason = pe.Message
		if kind == protocol.EffectStartOver && (task.WorkspaceReused || task.RequiredHostID != "") && task.Block == protocol.BlockWorkspaceUnavailable && !task.Lifecycle.Terminal() {
			// A persisted question from an older server can still offer this
			// effect. Keep its answer as a fact and replace it with valid choices.
			resource, loadErr := store.GetWorkspaceByTask(ctx, tx.Q(), task.ID)
			if loadErr != nil {
				return nil, nil, loadErr
			}
			replacement := domain.AskAboutWorkspaceAgain(task, resource.HostID, "holds its fixed workspace")
			_, events, applyErr := applyDecision(ctx, x, tx, row, replacement, map[string]any{"on": "unsupported_fixed_workspace_recovery", "decision_id": d.DecisionID})
			return res, events, applyErr
		}
		return res, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	_, events, err := applyDecision(ctx, x, tx, row, dec, map[string]any{"on": "answered", "decision_id": d.DecisionID})
	if err != nil {
		return nil, nil, err
	}
	res.Applied = true
	return res, events, nil
}

func nonEmpty(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
