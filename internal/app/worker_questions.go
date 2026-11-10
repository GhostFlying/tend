package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AskOwner opens a durable question without ending or releasing the run.
type AskOwner struct{}

func (AskOwner) Operation() string { return protocol.OpRunsAskOwner }
func (AskOwner) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the run itself may ask the owner")
	}
	rc, err := loadRun(ctx, q, x.Principal.Subject)
	if err != nil {
		return err
	}
	if x.Principal.Role != rc.run.Role {
		return protocol.Errorf(protocol.CodeForbidden, "run role does not match")
	}
	if desiredFor(rc.run, rc.action, rc.task) == protocol.DesiredStopped {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the question's run no longer applies")
	}
	runtime, err := store.GetHostRuntime(ctx, q, rc.run.HostID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err != nil || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityWorkerQuestions) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner has not negotiated worker-questions-v1")
	}
	return nil
}
func (AskOwner) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.AskOwnerPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.QuestionKey) == "" || len(p.QuestionKey) > 128 || strings.TrimSpace(p.Question) == "" || !utf8.ValidString(p.Question) || !utf8.ValidString(p.Evidence) || !utf8.ValidString(p.QuestionKey) || len(p.Options) > 10 || slices.Contains(p.Options, "") || len(dedupe(p.Options)) != len(p.Options) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "question_key, question and up to 10 distinct options are required")
	}
	for _, option := range p.Options {
		if !utf8.ValidString(option) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "options must be UTF-8")
		}
	}
	if err := validateDecisionIssues(p.Issues); err != nil {
		return Outcome{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return Outcome{}, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	rc, err := loadRun(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return Outcome{}, err
	}
	existing, err := store.GetWorkerQuestionByKey(ctx, tx.Q(), rc.run.RunID, p.QuestionKey)
	if err == nil {
		if existing.PayloadHash != hash {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "question_key already names different question content")
		}
		result, err := store.WorkerQuestion(ctx, tx.Q(), existing.DecisionID)
		return Outcome{Result: result}, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	currentOwner, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), rc.task.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	currentRun, err := store.RunIssueResponsibilityCurrent(ctx, tx.Q(), rc.run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if !currentOwner || !currentRun {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "the run's Issue responsibility changed; retain its facts without opening a new owner question")
	}
	questions, err := store.ListWorkerQuestions(ctx, tx.Q(), rc.run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	for _, question := range questions {
		if question.Status == store.DecisionOpen {
			pe := protocol.Errorf(protocol.CodeInvalidTransition, "the run already has an open owner question; resume its question_key")
			pe.Details, _ = json.Marshal(question)
			return Outcome{}, pe
		}
	}
	options, err := json.Marshal(p.Options)
	if err != nil {
		return Outcome{}, err
	}
	id := x.IDs.New(protocol.PrefixDecision)
	message := x.IDs.New(protocol.PrefixMessage)
	if err := tx.InsertDecision(ctx, id, rc.task.TaskID, p.Question, string(options), "{}", nil, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if err := tx.InsertWorkerQuestion(ctx, store.WorkerQuestionRow{DecisionID: id, RunID: rc.run.RunID, ActionID: rc.action.ActionID, SpecVersion: rc.action.TargetSpecVersion, QuestionKey: p.QuestionKey, PayloadHash: hash, Evidence: p.Evidence, Issues: p.Issues}); err != nil {
		return Outcome{}, err
	}
	var handoff bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT handoff_allowed FROM run_question_contracts WHERE run_id=?`, rc.run.RunID).Scan(&handoff); err != nil {
		return Outcome{}, err
	}
	if handoff {
		if _, err := tx.Exec(ctx, `INSERT INTO run_question_handoffs(run_id,decision_id) VALUES(?,?)`, rc.run.RunID, id); err != nil {
			return Outcome{}, err
		}
	}
	// The notification is materialized by persistOutcome in this same transaction.
	if err := tx.SetDecisionAskedMessage(ctx, id, message); err != nil {
		return Outcome{}, err
	}
	question, err := store.WorkerQuestion(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: question, Events: []Event{{ObjectType: "decision", ObjectID: id, ObjectVersion: 1, Kind: "decision.opened", Body: map[string]any{"task_id": rc.task.TaskID, "run_id": rc.run.RunID}}}}, nil
}

// WorkerQuestions returns persistent questions scoped to the credential's run.
// A stale run sees cancellation, never a usable answer to resume obsolete work.
func (a *App) WorkerQuestions(ctx context.Context, p protocol.Principal) ([]protocol.WorkerQuestion, error) {
	if p.Kind != protocol.PrincipalRun {
		return nil, protocol.Errorf(protocol.CodeForbidden, "run identity is required")
	}
	var out []protocol.WorkerQuestion
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rc, err := loadRun(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		if p.Role != rc.run.Role {
			return protocol.Errorf(protocol.CodeForbidden, "run role does not match")
		}
		out, err = store.ListWorkerQuestions(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		currentOwner, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), rc.task.TaskID)
		if err != nil {
			return err
		}
		currentRun, err := store.RunIssueResponsibilityCurrent(ctx, tx.Q(), rc.run.RunID)
		if err != nil {
			return err
		}
		if desiredFor(rc.run, rc.action, rc.task) == protocol.DesiredStopped || !currentOwner || !currentRun {
			for i := range out {
				out[i].Status = store.DecisionCancelled
				out[i].Answer = nil
				out[i].Inputs = nil
			}
		}
		return nil
	})
	return out, err
}

func (a *App) WorkerQuestion(ctx context.Context, p protocol.Principal, id string) (protocol.WorkerQuestion, error) {
	questions, err := a.WorkerQuestions(ctx, p)
	if err != nil {
		return protocol.WorkerQuestion{}, err
	}
	for _, q := range questions {
		if q.DecisionID == id {
			return q, nil
		}
	}
	return protocol.WorkerQuestion{}, protocol.Errorf(protocol.CodeNotFound, "worker question does not exist in this run")
}

func checkQuestionRunRecovery(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime) error {
	supportsQuestions := runtime != nil && slices.Contains(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	supportsContext := runtime != nil && slices.Contains(runtime.Capabilities, protocol.CapabilityWorkerQuestionContext)
	supportsSupervisor := runtime != nil && slices.Contains(runtime.Capabilities, protocol.CapabilitySupervisorQuestions)
	supportsHandoffs := runtime != nil && slices.Contains(runtime.Capabilities, protocol.CapabilityQuestionHandoffs)
	if supportsQuestions && supportsContext && supportsSupervisor && supportsHandoffs {
		return nil
	}
	runs, err := store.ListOpenRunsOnHost(ctx, q, host)
	if err != nil {
		return err
	}
	for _, run := range runs {
		rc, err := loadRun(ctx, q, run.RunID)
		if err != nil {
			return err
		}
		if desiredFor(run, rc.action, rc.task) == protocol.DesiredStopped {
			continue
		}
		if !supportsHandoffs {
			var enabled bool
			if err := q.QueryRowContext(ctx, `SELECT handoff_allowed FROM run_question_contracts WHERE run_id=?`, run.RunID).Scan(&enabled); err != nil {
				return err
			}
			if enabled {
				return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires issue-question-handoffs-v1 to recover this run")
			}
		}
		if !supportsSupervisor {
			var supervisor bool
			if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT supervisor_allowed FROM run_question_contracts WHERE run_id=?),0)`, run.RunID).Scan(&supervisor); err != nil {
				return err
			}
			if supervisor {
				return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires supervisor-questions-v1 to recover this run's frozen answer contract")
			}
		}
		questions, err := store.ListWorkerQuestions(ctx, q, run.RunID)
		if err != nil {
			return err
		}
		if len(questions) > 0 && !supportsQuestions {
			return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires worker-questions-v1 to recover a run with owner questions")
		}
		for _, question := range questions {
			if len(question.Issues) > 0 && !supportsContext {
				return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires worker-question-context-v1 to recover structured questions")
			}
		}
	}
	return nil
}

// cancelObsoleteQuestions runs after task, run or Issue changes, in the originating
// transaction. It removes stale questions from attention without inventing answers.
func cancelObsoleteQuestions(ctx context.Context, x *Exec, tx *store.Tx, out *Outcome) error {
	relevant := false
	for _, e := range out.Events {
		relevant = relevant || e.ObjectType == "task" || e.ObjectType == "run" || e.ObjectType == "issue"
	}
	if !relevant {
		return nil
	}
	ids, err := tx.CancelObsoleteWorkerQuestions(ctx, x.Now.UnixMilli())
	if err != nil {
		return err
	}
	ruleIDs, err := tx.CancelObsoleteTaskRuleQuestions(ctx, x.Now.UnixMilli())
	if err != nil {
		return err
	}
	ids = append(ids, ruleIDs...)
	for _, id := range ids {
		out.Events = append(out.Events, Event{ObjectType: "decision", ObjectID: id, Kind: "decision.cancelled", Body: map[string]any{"reason": "the source task or run no longer applies"}})
	}
	return nil
}

// recordWorkerAnswerSource preserves direct panel answers in chat without
// queuing another owner turn; the decision event performs the coordination.
func recordDecisionAnswerSource(ctx context.Context, x *Exec, tx *store.Tx, d store.DecisionRow, p *protocol.AnswerDecisionPayload) ([]Event, error) {
	if d.Worker != nil {
		rc, err := loadRun(ctx, tx.Q(), d.Worker.RunID)
		if err != nil {
			return nil, err
		}
		handoff, err := store.QuestionHandoffCurrent(ctx, tx.Q(), rc.run.RunID)
		if err != nil {
			return nil, err
		}
		if desiredFor(rc.run, rc.action, rc.task) == protocol.DesiredStopped && !handoff {
			return nil, protocol.Errorf(protocol.CodeStaleRevision, "the question's run no longer applies")
		}
	}
	if p.SourceMessageID != "" {
		_, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID)
		if err != nil {
			return nil, errNotFound(err, "owner answer message does not exist")
		}
		ownerAuthority, err := store.HasOwnerMessageAuthority(ctx, tx.Q(), p.SourceMessageID)
		if err != nil {
			return nil, err
		}
		if !ownerAuthority {
			return nil, protocol.Errorf(protocol.CodeForbidden, "worker answers require an actual owner message")
		}
		return nil, nil
	}
	if x.Principal.Kind != protocol.PrincipalOwner {
		return nil, protocol.Errorf(protocol.CodeForbidden, "worker answers require an actual owner message")
	}
	author := x.messageAuthor()
	body := p.Option
	if p.Text != "" {
		if body != "" {
			body += "\n\n"
		}
		body += p.Text
	}
	if x.interactionOrigin != nil && x.interactionOrigin.RawBody != "" {
		body = x.interactionOrigin.RawBody
	}
	id := x.IDs.New(protocol.PrefixMessage)
	reply := d.AskedMessageID
	if x.interactionOrigin != nil && x.interactionOrigin.ReplyTo != "" {
		reply = x.interactionOrigin.ReplyTo
	}
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: author, Body: body, ReplyTo: reply, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return nil, err
	}
	if err := x.recordMessageAuthority(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := registerInteractionAnswerInput(ctx, x, tx, id, d.AskedMessageID); err != nil {
		return nil, err
	}
	if err := tx.BindMessage(ctx, id, "decision", d.DecisionID, "answers"); err != nil {
		return nil, err
	}

	p.SourceMessageID = id
	events := []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": author, "reply_to": d.AskedMessageID}}}
	if author == "owner" || x.Actor != nil {
		events = append(events, Event{ObjectType: "message", ObjectID: id, Kind: "conversation.message_state", Body: map[string]any{"state": protocol.MessageReplied}})
	}
	return events, nil
}

func assignmentQuestions(ctx context.Context, q store.Querier, as *protocol.Assignment, host string) error {
	rt, err := store.GetHostRuntime(ctx, q, host)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	as.WorkerQuestions = err == nil && slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	if err = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT supervisor_allowed FROM run_question_contracts WHERE run_id=?),0)`, as.RunID).Scan(&as.SupervisorQuestions); err != nil {
		return err
	}
	if as.SupervisorQuestions && as.Desired != protocol.DesiredStopped && !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilitySupervisorQuestions) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner cannot recover the frozen supervisor-question contract without supervisor-questions-v1")
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT handoff_allowed FROM run_question_contracts WHERE run_id=?),0)`, as.RunID).Scan(&as.QuestionHandoffs); err != nil {
		return err
	}
	if as.QuestionHandoffs && as.Desired != protocol.DesiredStopped && !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityQuestionHandoffs) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner cannot recover the frozen question handoff contract")
	}
	as.QuestionContext = err == nil && slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityWorkerQuestionContext)
	enabled, e := store.RunMessagesEnabled(ctx, q, as.RunID)
	if e != nil {
		return e
	}
	as.RunMessages = enabled && err == nil && slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityRunMessages)
	if as.WorkerQuestions {
		as.Questions, err = store.ListWorkerQuestions(ctx, q, as.RunID)
		if err != nil {
			return fmt.Errorf("app: assignment questions: %w", err)
		}
	}
	continuation, questions, e := store.QuestionContinuationForRun(ctx, q, as.RunID)
	if e != nil {
		return e
	}
	as.QuestionContinuation = continuation
	for i := range questions {
		questions[i].Inputs = nil
		questions[i].InputRevision = 0
	}
	as.Questions = append(questions, as.Questions...)

	return nil
}
