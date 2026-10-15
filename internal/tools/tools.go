// Package tools is the main agent's tool surface (D20): the only way the
// main agent changes the ledger. Tools are registered with the harness as
// dynamic tools in the "tend" namespace and answered here.
//
// The caller's identity comes from the connection, never from the
// arguments: the main thread manager passes the harness instance, thread
// and the conversation message the turn handles. A call's request ID is
// derived from its native identity (D28), so a call the harness repeats
// replays the original result, and planning writes carry the message and a
// commit sequence, so a turn repeated with new call IDs is caught as a
// duplicate effect.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// Call is one tool call from the main agent.
type Call struct {
	// Instance and Thread identify the harness and thread the call came
	// through; they form the caller's principal.
	Instance string
	Thread   string
	// Turn and CallID complete the call's native identity.
	Turn   string
	CallID string
	// SourceMessageID is the conversation message the turn handles.
	SourceMessageID string
	// ParentRequestID is the request that delivered that message.
	ParentRequestID string
	Tool            string
	Arguments       json.RawMessage
}

// Service answers the main agent's tool calls.
type Service struct {
	app   *app.App
	store *store.Store
	log   *slog.Logger
}

// New returns a Service. The store is used for reads only.
func New(a *app.App, s *store.Store, log *slog.Logger) *Service {
	return &Service{app: a, store: s, log: log}
}

type tool struct {
	name, description string
	run               func(s *Service, ctx context.Context, c Call) (any, error)
}

var tools = []tool{
	{"tasks_list", "List tasks with their state. Unfinished tasks come first.", (*Service).tasksList},
	{"tasks_read", "Read one task in full: spec, actions, runs, artifacts, verdicts and decisions.", (*Service).tasksRead},
	{"tasks_create", "Create a task from an explicit request of the owner.", (*Service).tasksCreate},
	{"tasks_update_intent", "Change a task's spec or priority as the owner asked.", (*Service).tasksUpdateIntent},
	{"actions_create", "Request another implementation or acceptance after the owner decided to retry.", (*Service).actionsCreate},
	{"decisions_open", "Ask the owner a question that blocks a decision you cannot make.", (*Service).decisionsOpen},
	{"decisions_resolve", "Record the answer the owner gave in the current message to an open decision.", (*Service).decisionsResolve},
	{"artifacts_read", "Read an artifact and the verdicts on it.", (*Service).artifactsRead},
	{"requests_read", "Look up the outcome of one of your earlier requests instead of repeating it.", (*Service).requestsRead},
	{"repos_list", "List the registered repositories, workspace templates and hosts.", (*Service).reposList},
}

// Specs returns the tools to register with the harness.
func Specs() []harness.ToolSpec {
	out := make([]harness.ToolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, harness.ToolSpec{Name: t.name, Description: t.description, InputSchema: json.RawMessage(schemas[t.name])})
	}
	return out
}

// answer is the JSON a tool returns to the model.
type answer struct {
	OK     bool            `json:"ok"`
	Result any             `json:"result,omitempty"`
	Error  *protocol.Error `json:"error,omitempty"`
	// Current is the task's state after a stale write, so the model can
	// decide again in the same turn.
	Current any `json:"current,omitempty"`
}

// Handle answers one tool call.
func (s *Service) Handle(ctx context.Context, c Call) harness.ToolResult {
	var t *tool
	for i := range tools {
		if tools[i].name == c.Tool {
			t = &tools[i]
		}
	}
	var a answer
	if t == nil {
		a.Error = protocol.Errorf(protocol.CodeInvalidRequest, "there is no tool %q", c.Tool)
	} else if res, err := t.run(s, ctx, c); err != nil {
		pe, ok := errors.AsType[*protocol.Error](err)
		if !ok {
			s.log.ErrorContext(ctx, "tool failed", "tool", c.Tool, "thread", c.Thread, "call", c.CallID, "err", err)
			pe = protocol.Errorf(protocol.CodeInternal, "internal error")
		}
		a.Error = pe
		if pe.Code == protocol.CodeStaleRevision {
			a.Current = s.currentFor(ctx, c)
		}
	} else {
		a.OK, a.Result = true, res
	}
	b, err := json.Marshal(a)
	if err != nil {
		return harness.ToolResult{Text: `{"ok":false,"error":{"code":"internal","message":"internal error"}}`}
	}
	return harness.ToolResult{Success: a.OK, Text: string(b)}
}

// currentFor reads the state of the task a stale call was about.
func (s *Service) currentFor(ctx context.Context, c Call) any {
	var args struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(c.Arguments, &args) != nil || args.TaskID == "" {
		return nil
	}
	var out any
	_ = s.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetTask(ctx, tx.Q(), args.TaskID)
		if err == nil {
			out = views.Summary(row)
		}
		return err
	})
	return out
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "arguments do not match the tool's schema: %v", err)
	}
	return nil
}

// execute runs a write under the call's native identity.
func (s *Service) execute(ctx context.Context, c Call, op string, target protocol.Target, revision *int64, payload any, cmd app.Command) (any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("tools: encode payload: %w", err)
	}
	requestID, err := s.app.NativeRequestID(ctx, "harness:"+c.Instance, c.Thread+"/"+c.Turn+"/"+c.CallID)
	if err != nil {
		return nil, err
	}
	env := &protocol.Envelope{
		RequestID: requestID, ParentRequestID: c.ParentRequestID, Operation: op, Target: target,
		ExpectedControlRevision: revision, Payload: body,
	}
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: c.Instance, Thread: c.Thread}
	res, err := s.app.Execute(ctx, principal, env, cmd)
	if err != nil {
		return nil, err
	}
	if res.Status == protocol.OpFailed {
		return nil, res.Error
	}
	return map[string]any{"request_id": res.RequestID, "status": res.Status, "outcome": res.Result}, nil
}

// requireMessage rejects a planning write outside a conversation turn.
func requireMessage(c Call) error {
	if c.SourceMessageID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "this tool can only be used while handling a message")
	}
	return nil
}

type createArgs struct {
	Title         string      `json:"title"`
	Spec          domain.Spec `json:"spec"`
	Priority      int64       `json:"priority"`
	WorkspaceName string      `json:"workspace_name"`
	CommitSeq     int64       `json:"commit_seq"`
}

func (s *Service) tasksCreate(ctx context.Context, c Call) (any, error) {
	var a createArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "tasks.create", protocol.Target{Type: "task"}, nil, map[string]any{
		"title": a.Title, "spec": a.Spec, "priority": a.Priority, "workspace_name": a.WorkspaceName,
		"source_message_id": c.SourceMessageID, "commit_seq": a.CommitSeq,
	}, app.CreateTask{})
}

type updateArgs struct {
	TaskID                  string       `json:"task_id"`
	ExpectedControlRevision int64        `json:"expected_control_revision"`
	Spec                    *domain.Spec `json:"spec"`
	Priority                *int64       `json:"priority"`
	CommitSeq               int64        `json:"commit_seq"`
}

func (s *Service) tasksUpdateIntent(ctx context.Context, c Call) (any, error) {
	var a updateArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	payload := map[string]any{"source_message_id": c.SourceMessageID, "commit_seq": a.CommitSeq}
	if a.Spec != nil {
		payload["spec"] = a.Spec
	}
	if a.Priority != nil {
		payload["priority"] = *a.Priority
	}
	return s.execute(ctx, c, "tasks.update_intent", protocol.Target{Type: "task", ID: a.TaskID}, &a.ExpectedControlRevision, payload, app.UpdateIntent{})
}

type actionArgs struct {
	TaskID                  string              `json:"task_id"`
	ExpectedControlRevision int64               `json:"expected_control_revision"`
	Kind                    protocol.ActionKind `json:"kind"`
	Generation              int64               `json:"generation"`
	CommitSeq               int64               `json:"commit_seq"`
}

func (s *Service) actionsCreate(ctx context.Context, c Call) (any, error) {
	var a actionArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "actions.create", protocol.Target{Type: "task", ID: a.TaskID}, &a.ExpectedControlRevision, map[string]any{
		"kind": a.Kind, "generation": a.Generation, "source_message_id": c.SourceMessageID, "commit_seq": a.CommitSeq,
	}, app.CreateAction{})
}

type openArgs struct {
	TaskID   string   `json:"task_id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func (s *Service) decisionsOpen(ctx context.Context, c Call) (any, error) {
	var a openArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "decisions.open", protocol.Target{Type: "decision"}, nil, map[string]any{
		"task_id": a.TaskID, "question": a.Question, "options": a.Options, "source_message_id": c.SourceMessageID,
	}, app.OpenDecision{})
}

type resolveArgs struct {
	DecisionID string `json:"decision_id"`
	Version    int64  `json:"version"`
	Option     string `json:"option"`
	Text       string `json:"text"`
}

func (s *Service) decisionsResolve(ctx context.Context, c Call) (any, error) {
	var a resolveArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	cmd := app.AnswerDecision{FromMessage: true}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "decision", ID: a.DecisionID}, nil, map[string]any{
		"version": a.Version, "option": a.Option, "text": a.Text, "source_message_id": c.SourceMessageID,
	}, cmd)
}
