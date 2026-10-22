// Package tools is the main agent's tool surface (D20): the only way the
// main agent changes the ledger. The hosted main agent gets the tools as
// harness dynamic tools; an agent the owner works in gets them through the
// tend MCP bridge.
//
// The caller's identity comes from the connection, never from the
// arguments. A hosted call's request ID is derived from its native identity
// (D28), so a call the harness repeats replays the original result, and
// its planning writes carry the message and a commit sequence, so a turn
// repeated with new call IDs is caught as a duplicate effect.
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

// Call is one tool call from a main agent.
type Call struct {
	// Principal is the caller, established by the connection.
	Principal protocol.Principal
	// RequestID is the call's request ID when the caller gives one.
	// Otherwise it is derived from Native, the harness's identity of the
	// call within the instance named by the principal.
	RequestID string
	Native    string
	// SourceMessageID is the conversation message the turn handles.
	SourceMessageID string
	// ParentRequestID is the request that delivered that message.
	ParentRequestID string
	Tool            string
	Arguments       json.RawMessage
}

// Snapshots shows the ledger as a full snapshot, for tend_context.
type Snapshots interface {
	Current(ctx context.Context, self protocol.Principal) (string, error)
}

// Service answers the main agent's tool calls.
type Service struct {
	app       *app.App
	store     *store.Store
	snapshots Snapshots
	log       *slog.Logger
}

// New returns a Service. The store is used for reads only.
func New(a *app.App, s *store.Store, snapshots Snapshots, log *slog.Logger) *Service {
	return &Service{app: a, store: s, snapshots: snapshots, log: log}
}

type tool struct {
	name, description string
	run               func(s *Service, ctx context.Context, c Call) (any, error)
}

var tools = []tool{
	{"tasks_list", "List tasks with their state. Unfinished tasks come first.", (*Service).tasksList},
	{"tasks_read", "Read one task in full: spec, actions, runs, artifacts, verdicts and decisions.", (*Service).tasksRead},
	{"tasks_create", "Create a task from an explicit request of the owner.", (*Service).tasksCreate},
	{"tasks_update_intent", "Change a task's spec, rating or project as the owner asked.", (*Service).tasksUpdateIntent},
	{"tasks_set_control", "Pause a task, or hand a paused task back to tend, as the owner asked. A paused task starts nothing new and its running work stops.", (*Service).tasksSetControl},
	{"tasks_cancel", "Cancel a task as the owner asked. Its running work stops and it never runs again.", (*Service).tasksCancel},
	{"actions_create", "Request another implementation or acceptance after the owner decided to retry.", (*Service).actionsCreate},
	{"decisions_open", "Ask the owner a question that blocks a decision you cannot make.", (*Service).decisionsOpen},
	{"decisions_resolve", "Record the answer the owner gave you to an open decision. For options of decisions tend opened, the result says what tend carried out.", (*Service).decisionsResolve},
	{"artifacts_read", "Read an artifact and the verdicts on it.", (*Service).artifactsRead},
	{"requests_read", "Look up the outcome of one of your earlier requests instead of repeating it.", (*Service).requestsRead},
	{"repos_list", "List the registered repositories, workspace templates and hosts.", (*Service).reposList},
	{"tend_context", "Show tend's whole ledger again as a full snapshot.", (*Service).tendContext},
	{"session_project", "Choose the project this session works in, as the owner asked, in place of the one tend chose from the repository you are in. An empty project shows every project.", (*Service).sessionProject},
}

// addonOnly names the tools that have meaning only in a session the owner
// works in; the hosted main agent does not get them.
var addonOnly = map[string]bool{"session_project": true}

// Specs returns the tools to register with the hosted main agent's
// harness.
func Specs() []harness.ToolSpec {
	out := make([]harness.ToolSpec, 0, len(tools))
	for _, t := range tools {
		if addonOnly[t.name] {
			continue
		}
		out = append(out, harness.ToolSpec{Name: t.name, Description: t.description, InputSchema: json.RawMessage(schemas[t.name])})
	}
	return out
}

// AddonSpecs returns the tools for agents the owner works in (D48). Their
// planning writes have no conversation message, so no commit_seq, and a
// new task must name its workspace: the unique name is what stops a model
// that calls tasks_create again from creating the task twice.
func AddonSpecs() []protocol.ToolSpec {
	out := make([]protocol.ToolSpec, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if err := json.Unmarshal([]byte(schemas[t.name]), &schema); err != nil {
			panic(fmt.Sprintf("tools: schema of %s: %v", t.name, err)) // the schemas are constants
		}
		props, _ := schema["properties"].(map[string]any)
		delete(props, "commit_seq")
		var required []string
		if r, ok := schema["required"].([]any); ok {
			for _, v := range r {
				if v != "commit_seq" {
					required = append(required, v.(string))
				}
			}
		}
		if t.name == "tasks_create" {
			required = append(required, "workspace_name")
		}
		if required != nil {
			schema["required"] = required
		}
		b, err := json.Marshal(schema)
		if err != nil {
			panic(fmt.Sprintf("tools: schema of %s: %v", t.name, err))
		}
		out = append(out, protocol.ToolSpec{Name: t.name, Description: t.description, InputSchema: b})
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
		if tools[i].name == c.Tool && (c.Principal.Addon() || !addonOnly[c.Tool]) {
			t = &tools[i]
		}
	}
	var a answer
	if t == nil {
		a.Error = protocol.Errorf(protocol.CodeInvalidRequest, "there is no tool %q", c.Tool)
	} else if res, err := t.run(s, ctx, c); err != nil {
		pe, ok := errors.AsType[*protocol.Error](err)
		if !ok {
			s.log.ErrorContext(ctx, "tool failed", "tool", c.Tool, "thread", c.Principal.Thread, "request_id", c.RequestID, "native", c.Native, "err", err)
			pe = protocol.Errorf(protocol.CodeInternal, "internal error")
		}
		a.Error = pe
		if pe.Code == protocol.CodeStaleRevision {
			a.Current = s.currentFor(ctx, c)
		} else if c.Tool == "tasks_create" && pe.Code == protocol.CodeInvalidRequest {
			// A taken workspace name names its task: the model may be
			// repeating its own creation.
			var d struct {
				TaskID string `json:"task_id"`
			}
			if json.Unmarshal(pe.Details, &d) == nil && d.TaskID != "" {
				a.Current = s.summary(ctx, d.TaskID)
			}
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
	return s.summary(ctx, args.TaskID)
}

// summary reads a task's summary, or nil.
func (s *Service) summary(ctx context.Context, taskID string) any {
	var out any
	_ = s.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetTask(ctx, tx.Q(), taskID)
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

// execute runs a write under the call's request ID, with the model's
// reason for it.
func (s *Service) execute(ctx context.Context, c Call, op string, target protocol.Target, revision *int64, reason string, payload any, cmd app.Command) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("tools: encode payload: %w", err)
	}
	requestID := c.RequestID
	if requestID == "" {
		if c.Native == "" {
			return nil, protocol.Errorf(protocol.CodeMissingRequestID, "the call has no request ID")
		}
		if requestID, err = s.app.NativeRequestID(ctx, "harness:"+c.Principal.Subject, c.Native); err != nil {
			return nil, err
		}
	}
	env := &protocol.Envelope{
		RequestID: requestID, ParentRequestID: c.ParentRequestID, Operation: op, Target: target,
		ExpectedControlRevision: revision, Payload: body, Reason: reason,
	}
	res, err := s.app.Execute(ctx, c.Principal, env, cmd)
	if err != nil {
		return nil, err
	}
	if res.Status == protocol.OpFailed {
		return nil, res.Error
	}
	return map[string]any{"request_id": res.RequestID, "status": res.Status, "outcome": res.Result}, nil
}

// requireMessage rejects a hosted planning write outside a conversation
// turn. Agents the owner works in have no conversation in tend.
func requireMessage(c Call) error {
	if c.SourceMessageID == "" && !c.Principal.Addon() {
		return protocol.Errorf(protocol.CodeInvalidRequest, "this tool can only be used while handling a message")
	}
	return nil
}

type createArgs struct {
	Title         string          `json:"title"`
	Spec          domain.Spec     `json:"spec"`
	Rating        protocol.Rating `json:"rating"`
	Project       string          `json:"project"`
	WorkspaceName string          `json:"workspace_name"`
	CommitSeq     int64           `json:"commit_seq"`
	Reason        string          `json:"reason"`
}

func (s *Service) tasksCreate(ctx context.Context, c Call) (any, error) {
	var a createArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	if c.Principal.Addon() && a.WorkspaceName == "" {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "workspace_name is required")
	}
	res, err := s.execute(ctx, c, "tasks.create", protocol.Target{Type: "task"}, nil, a.Reason, map[string]any{
		"title": a.Title, "spec": a.Spec, "rating": a.Rating, "project": a.Project, "workspace_name": a.WorkspaceName,
		"source_message_id": c.SourceMessageID, "commit_seq": a.CommitSeq,
	}, app.CreateTask{})
	if err == nil && a.Rating == protocol.RatingNone {
		// The owner is present while a task is dispatched (D52), so this
		// is the time to ask.
		res["note"] = unratedNote
	}
	return res, err
}

// unratedNote asks the agent to have the owner rate an unrated task.
const unratedNote = "The task is unrated and queues as p2. Ask the owner how it ranks: " +
	"p0 urgent, ahead of everything; p1 high; p2 normal; p3 low. Then set it with tasks_update_intent."

type updateArgs struct {
	TaskID                  string           `json:"task_id"`
	ExpectedControlRevision int64            `json:"expected_control_revision"`
	Spec                    *domain.Spec     `json:"spec"`
	Rating                  *protocol.Rating `json:"rating"`
	Project                 *string          `json:"project"`
	CommitSeq               int64            `json:"commit_seq"`
	Reason                  string           `json:"reason"`
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
	if a.Rating != nil {
		payload["rating"] = *a.Rating
	}
	if a.Project != nil {
		payload["project"] = *a.Project
	}
	return s.execute(ctx, c, "tasks.update_intent", protocol.Target{Type: "task", ID: a.TaskID}, &a.ExpectedControlRevision, a.Reason, payload, app.UpdateIntent{})
}

type controlArgs struct {
	TaskID                  string               `json:"task_id"`
	ExpectedControlRevision int64                `json:"expected_control_revision"`
	Mode                    protocol.ControlMode `json:"mode"`
	Reason                  string               `json:"reason"`
}

func (s *Service) tasksSetControl(ctx context.Context, c Call) (any, error) {
	var a controlArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "control.set_mode", protocol.Target{Type: "task", ID: a.TaskID}, &a.ExpectedControlRevision, a.Reason,
		protocol.SetControlModePayload{Mode: a.Mode}, app.SetControlMode{})
}

type cancelArgs struct {
	TaskID                  string `json:"task_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
	Reason                  string `json:"reason"`
}

func (s *Service) tasksCancel(ctx context.Context, c Call) (any, error) {
	var a cancelArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "tasks.cancel", protocol.Target{Type: "task", ID: a.TaskID}, &a.ExpectedControlRevision, a.Reason,
		map[string]any{}, app.CancelTask{})
}

type actionArgs struct {
	TaskID                  string              `json:"task_id"`
	ExpectedControlRevision int64               `json:"expected_control_revision"`
	Kind                    protocol.ActionKind `json:"kind"`
	Generation              int64               `json:"generation"`
	CommitSeq               int64               `json:"commit_seq"`
	Reason                  string              `json:"reason"`
}

func (s *Service) actionsCreate(ctx context.Context, c Call) (any, error) {
	var a actionArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "actions.create", protocol.Target{Type: "task", ID: a.TaskID}, &a.ExpectedControlRevision, a.Reason, map[string]any{
		"kind": a.Kind, "generation": a.Generation, "source_message_id": c.SourceMessageID, "commit_seq": a.CommitSeq,
	}, app.CreateAction{})
}

type openArgs struct {
	TaskID   string   `json:"task_id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Reason   string   `json:"reason"`
}

func (s *Service) decisionsOpen(ctx context.Context, c Call) (any, error) {
	var a openArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "decisions.open", protocol.Target{Type: "decision"}, nil, a.Reason, map[string]any{
		"task_id": a.TaskID, "question": a.Question, "options": a.Options, "source_message_id": c.SourceMessageID,
	}, app.OpenDecision{})
}

type resolveArgs struct {
	DecisionID string `json:"decision_id"`
	Version    int64  `json:"version"`
	Option     string `json:"option"`
	Text       string `json:"text"`
	Reason     string `json:"reason"`
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
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "decision", ID: a.DecisionID}, nil, a.Reason, map[string]any{
		"version": a.Version, "option": a.Option, "text": a.Text, "source_message_id": c.SourceMessageID,
	}, cmd)
}
