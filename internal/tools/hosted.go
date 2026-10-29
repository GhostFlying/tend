package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type hostedMutationArgs struct {
	TargetID                string          `json:"target_id,omitempty"`
	ExpectedControlRevision *int64          `json:"expected_control_revision,omitempty"`
	EffectSeq               int64           `json:"effect_seq"`
	Payload                 json.RawMessage `json:"payload"`
}
type hostedReadArgs struct {
	IssueID  string `json:"issue_id,omitempty"`
	Revision int64  `json:"revision,omitempty"`
}
type hostedEmptyPayload struct{}

type hostedMutation struct {
	name, description, target string
	payload                   any
	command                   app.Command
	main                      bool
}

var hostedMutations = []hostedMutation{
	{"conversation_associate_issues", "Classify this exact message into canonical Issues or explicitly leave it unassigned. Preserve owner corrections; this acknowledges routing only and never clears Issue source-review duties or authorizes execution.", "message", protocol.SetIssueMessageAssociationsPayload{}, app.SetIssueMessageAssociations{}, true},
	{"issues_create", "Delegate a vague goal to a concrete registered Issue manager. Managers create only child Issues within their own scope.", "issue", protocol.CreateIssuePayload{}, app.CanonicalOwnerIssueCommand{Action: "create"}, true},
	{"issues_assign", "Fence a concrete Issue assignee before reconciling and enabling its replacement. Does not stop independent workers.", "issue", protocol.IssueAssignmentPayload{}, app.HostedIssueAssignment{}, true},
	{"issues_finish_assignment", "Enable a fenced assignee only after the previous manager's actual operations are reconciled.", "issue", protocol.FinishIssueAssignmentPayload{}, app.HostedIssueAssignment{Finish: true}, true},
	{"issues_update", "Revise only this Issue definition; task specifications stay frozen until explicitly refreshed.", "issue", protocol.UpdateIssuePayload{}, app.CanonicalOwnerIssueCommand{Action: "update"}, false},
	{"issues_pause", "Stop new dispatch for this Issue; active worker contracts continue.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "pause"}, false},
	{"issues_resume", "Resume this Issue's new dispatch under its current assignment.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "resume"}, false},
	{"issues_cancel", "Record cancellation intent before settling outstanding tasks and obligations.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "cancel"}, false},
	{"issues_finish_cancel", "Finish cancellation with explicit source/follow-on dispositions after real execution has settled.", "issue", protocol.IssueEndpointPayload{}, app.CanonicalOwnerIssueCommand{Action: "finish_cancel"}, false},
	{"issues_close", "Close only this Issue after child/task results and obligations have been explicitly reviewed.", "issue", protocol.IssueEndpointPayload{}, app.CanonicalOwnerIssueCommand{Action: "close"}, false},
	{"issues_reopen", "Explicitly reopen this responsibility; never automatically replay a finished run.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "reopen"}, false},
	{"issues_dependencies", "Revise this Issue's explicit completion dependencies without adding task execution ownership.", "issue", protocol.SetIssueDependenciesPayload{}, app.CanonicalOwnerIssueCommand{Action: "dependencies"}, false},
	{"issues_review_sources", "Review named late source shares on a terminal Issue while retaining its terminal history.", "issue", protocol.ReviewIssueSourcesPayload{}, app.CanonicalOwnerIssueCommand{Action: "review_sources"}, false},
	{"issues_review_completions", "Acknowledge exact primary task completion facts on this Issue without changing acceptance results.", "issue", protocol.ReviewIssueCompletionsPayload{}, app.CanonicalOwnerIssueCommand{Action: "review_completions"}, false},
	{"issues_create_task", "Delegate all investigation and execution through a frozen no-stage or explicitly staged task with independent acceptance. Never execute native work yourself.", "issue", protocol.CreateIssueTaskPayload{}, app.CanonicalOwnerTaskCreate{}, false},
	{"issues_update_task", "Explicitly refresh an existing primary task contract through its current revision and Issue epoch; active runs retain their frozen contracts.", "task", protocol.UpdateIssueTaskPayload{}, app.CanonicalOwnerTaskUpdate{}, false},
	{"tasks_cancel", "Cancel only this Issue's primary task through its current revision and existing run reconciliation rules.", "task", hostedEmptyPayload{}, app.CancelTask{}, false},
	{"tasks_rate", "Set this Issue's primary task priority through its current revision.", "task", protocol.RateTaskPayload{}, app.RateTask{}, false},
	{"actions_create", "Request only an implement or independent acceptance generation for this Issue's primary task; native takeover and handback remain owner-only.", "task", protocol.CreateActionPayload{}, app.CreateAction{}, false},
	{"issues_context_fact", "Record or replace a sourced fact only in this Issue. Original attribution is preserved; agent narration is not owner authority.", "issue", protocol.ContextFact{}, app.HostedIssueContextFact{}, false},
	{"issues_context_review", "Review only this Issue's exact source versions and context revision. Uncertainty and no new facts are valid; no global baseline or task-spec refresh is manufactured.", "issue", protocol.IssueContextReview{}, app.ReviewHostedIssueContext{}, false},
	{"issues_turn_commit", "Commit this manager's explicit current source/context review; native completion remains separate.", "logical_turn", protocol.LogicalTurnCommit{}, app.CommitLogicalTurn{}, false},
}

func hostedMutationSchema(payload any) json.RawMessage {
	var root, body map[string]any
	if err := json.Unmarshal([]byte(argumentSchema(hostedMutationArgs{})), &root); err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(argumentSchema(payload)), &body); err != nil {
		panic(err)
	}
	root["definitions"] = body["definitions"]
	delete(body, "definitions")
	root["properties"].(map[string]any)["payload"] = body
	b, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return exportSchema(b)
}

// NamedSpecs is a role capability surface, not a category permission grant.
func NamedSpecs(purpose agentconfig.Purpose) []harness.ToolSpec {
	var out []harness.ToolSpec
	for _, m := range hostedMutations {
		if purpose == agentconfig.Main && !m.main || purpose == agentconfig.IssueManager && m.main && m.name != "issues_create" || purpose == agentconfig.Consultation {
			continue
		}
		out = append(out, harness.ToolSpec{Name: m.name, Description: m.description, InputSchema: hostedMutationSchema(m.payload)})
	}
	out = append(out, harness.ToolSpec{Name: "issues_read", Description: "Read an Issue and its primary execution/completion facts; managers are limited to their Issue and descendants.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedReadArgs{})))})
	out = append(out, harness.ToolSpec{Name: "agents_hosted_list", Description: "Read accepted hosted identities, roles and configuration revision. Private instructions and local credentials are not exposed.", InputSchema: exportSchema(json.RawMessage(argumentSchema(turnReadArgs{})))})
	if purpose == agentconfig.IssueManager {
		out = append(out, harness.ToolSpec{Name: "issues_turn_read", Description: "Read this manager turn's exact source versions and context share.", InputSchema: exportSchema(json.RawMessage(argumentSchema(turnReadArgs{})))})
	}
	for _, spec := range Specs() {
		if namedLegacyRead(purpose, spec.Name) {
			if purpose == agentconfig.Main && spec.Name != "background_sources" && spec.Name != "background_checkin" {
				spec = HostedOwnerSourceSpec(spec)
			}
			out = append(out, spec)
		}
	}
	return out
}
func namedLegacyRead(purpose agentconfig.Purpose, name string) bool {
	if purpose == agentconfig.Main {
		switch name {
		case "tend_context", "repos_list", "tasks_list", "tasks_read", "actions_list", "runs_read", "artifacts_read", "decisions_list", "decisions_read", "decisions_resolve", "decisions_explain", "conversation_read", "conversation_turn_read", "conversation_turn_commit", "conversation_turn_wait", "conversation_turn_adopt", "agent_conversations_list", "agent_rollout_read", "background_sources", "background_checkin":
			return true
		}
	}
	if purpose == agentconfig.IssueManager {
		switch name {
		case "repos_list", "tasks_read", "actions_list", "runs_read", "artifacts_read", "work_item_context_read", "background_evidence_read", "background_sources", "agent_rollout_read", "conversation_read":
			return true
		}
	}
	return false
}
func hostedAnswer(value any, err error) harness.ToolResult {
	out := answer{OK: err == nil, Result: value}
	if err != nil {
		if !errors.As(err, &out.Error) {
			out.Error = protocol.Errorf(protocol.CodeInternal, "hosted tool failed")
		}
	}
	b, e := json.Marshal(out)
	if e != nil {
		return harness.ToolResult{Text: "Cannot encode hosted tool result"}
	}
	return harness.ToolResult{Success: out.OK, Text: string(b)}
}
func (s *Service) handleHosted(ctx context.Context, c Call) (harness.ToolResult, bool) {
	execution, found, err := s.app.HostedExecution(ctx, c.executionRequest())
	if err != nil {
		return hostedAnswer(nil, err), true
	}
	if !found {
		return harness.ToolResult{}, false
	}
	fail := func(err error) (harness.ToolResult, bool) { return hostedAnswer(nil, err), true }
	var native store.MainTurn
	err = s.store.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		native, e = store.GetMainTurn(ctx, tx.Q(), execution.ID)
		return e
	})
	if err != nil {
		return fail(err)
	}
	if native.ThreadID != c.Principal.Thread || native.TurnID != c.TurnID || execution.ConversationID != store.ConversationID(c.Principal.ConversationID) {
		return fail(protocol.Errorf(protocol.CodeForbidden, "caller does not own the admitted execution"))
	}
	repair, err := s.app.IsRepairExecution(ctx, execution.ID)
	if err != nil {
		return fail(err)
	}
	if repair && !namedRepairTool(execution.Selection.Purpose, c.Tool) {
		return fail(protocol.Errorf(protocol.CodeForbidden, "metadata repair cannot perform business operations"))
	}
	for _, m := range hostedMutations {
		if c.Tool != m.name {
			continue
		}
		if execution.Selection.Purpose == agentconfig.Consultation || execution.Selection.Purpose == agentconfig.Main && !m.main {
			return fail(protocol.Errorf(protocol.CodeForbidden, "this role must delegate Issue execution"))
		}
		var args hostedMutationArgs
		if err := decode(c.Arguments, &args); err != nil {
			return fail(err)
		}
		command := m.command
		if c.Tool == "issues_turn_commit" {
			command = app.CommitLogicalTurn{RequestID: execution.ID}
			if args.TargetID == "" {
				args.TargetID = execution.ID
			}
		}
		if m.target == "issue" && args.TargetID == "" && c.Tool != "issues_create" {
			args.TargetID = execution.IssueID
		}
		cmd := app.HostedCoordinationCommand{Command: command, ExecutionID: execution.ID, EffectSeq: args.EffectSeq}
		request := c.RequestID
		if request == "" {
			if c.Native == "" {
				return fail(protocol.Errorf(protocol.CodeMissingRequestID, "tool callback has no native identity"))
			}
			var err error
			request, err = s.app.NativeRequestID(ctx, "harness:"+c.Principal.Subject, c.Native)
			if err != nil {
				return fail(err)
			}
		}
		env := &protocol.Envelope{RequestID: request, Operation: command.Operation(), Target: protocol.Target{Type: m.target, ID: args.TargetID}, ExpectedControlRevision: args.ExpectedControlRevision, Payload: args.Payload}
		result, err := s.app.Execute(ctx, c.Principal, env, cmd)
		if err != nil {
			return fail(err)
		}
		if result.Error != nil {
			return fail(result.Error)
		}
		return hostedAnswer(map[string]any{"request_id": result.RequestID, "status": result.Status, "outcome": result.Result}, nil), true
	}
	if c.Tool == "agents_hosted_list" {
		v, e := s.app.HostedAgentInventory(ctx)
		return hostedAnswer(v, e), true
	}
	if c.Tool == "issues_turn_read" && execution.IssueID != "" {
		v, e := s.app.LogicalTurn(ctx, execution.ID)
		return hostedAnswer(v, e), true
	}
	if c.Tool == "issues_read" {
		var p hostedReadArgs
		if err := decode(c.Arguments, &p); err != nil {
			return fail(err)
		}
		if p.IssueID == "" {
			p.IssueID = execution.IssueID
		}
		var value any
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			if execution.IssueID != "" {
				var allowed bool
				if err := tx.Q().QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (SELECT ? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN descendants d ON c.parent_issue_id=d.id) SELECT EXISTS(SELECT 1 FROM descendants WHERE id=?)`, execution.IssueID, p.IssueID).Scan(&allowed); err != nil {
					return err
				}
				if !allowed {
					return protocol.Errorf(protocol.CodeForbidden, "Issue is outside manager scope")
				}
			}
			issue, e := views.CanonicalIssue(ctx, tx.Q(), p.IssueID, p.Revision)
			if e != nil {
				return e
			}
			visible := issue.SourceReviews[:0]
			for _, source := range issue.SourceReviews {
				admitted, err := store.OwnerInputAdmitted(ctx, tx.Q(), source.MessageID)
				if err != nil {
					return err
				}
				if !admitted {
					continue
				}
				item := protocol.ConversationItem{MessageID: source.MessageID, Author: source.Author, Body: source.Body}
				if err := store.HideUndeliveredOwner(ctx, tx.Q(), &item); err != nil {
					return err
				}
				source.Body = item.Body
				visible = append(visible, source)
			}
			issue.SourceReviews = visible
			value = issue
			return nil
		})
		return hostedAnswer(value, err), true
	}
	if !namedLegacyRead(execution.Selection.Purpose, c.Tool) {
		return fail(protocol.Errorf(protocol.CodeForbidden, "tool is unavailable to this named role"))
	}
	if execution.IssueID != "" {
		if c.Tool == "conversation_read" {
			var p conversationArgs
			if err := decode(c.Arguments, &p); err != nil {
				return fail(err)
			}
			if p.ConversationID != "" && p.ConversationID != execution.ConversationID {
				return fail(protocol.Errorf(protocol.CodeForbidden, "conversation is outside this Issue"))
			}
			p.ConversationID = execution.ConversationID
			c.Arguments, _ = json.Marshal(p)
		}
		var ids struct {
			TaskID     string `json:"task_id"`
			RunID      string `json:"run_id"`
			ArtifactID string `json:"artifact_id"`
		}
		_ = json.Unmarshal(c.Arguments, &ids)
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			task := ids.TaskID
			if ids.RunID != "" {
				run, e := store.GetRun(ctx, tx.Q(), ids.RunID)
				if e != nil {
					return e
				}
				task = run.TaskID
			}
			if ids.ArtifactID != "" {
				artifact, e := store.GetArtifact(ctx, tx.Q(), ids.ArtifactID)
				if e != nil {
					return e
				}
				task = artifact.TaskID
			}
			if task != "" {
				allowed, e := hostedTaskResultReadable(ctx, tx.Q(), task, execution.IssueID)
				if e != nil {
					return e
				}
				if !allowed {
					return protocol.Errorf(protocol.CodeForbidden, "task execution facts are outside this Issue")
				}
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
	}
	c.hostedRead = true
	return s.Handle(ctx, c), true
}

// Repair capabilities remain a subset of the admitted role's tool surface.
func namedRepairTool(purpose agentconfig.Purpose, name string) bool {
	switch name {
	case "conversation_associate_issues":
		return purpose == agentconfig.Main
	case "issues_read", "agents_hosted_list":
		return true
	case "issues_turn_read", "issues_context_fact", "issues_context_review", "issues_turn_commit":
		return purpose == agentconfig.IssueManager
	default:
		return repairTools[name] && namedLegacyRead(purpose, name)
	}
}

// hostedTaskResultReadable adds delegated descendant results to the existing
// explicitly linked evidence scope. It never grants task or source control.
func hostedTaskResultReadable(ctx context.Context, q store.Querier, taskID, issueID string) (bool, error) {
	linked, err := store.TaskIssueEvidenceReadable(ctx, q, taskID, issueID)
	if err != nil || linked {
		return linked, err
	}
	var descendant bool
	err = q.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (
 SELECT issue_id FROM issue_controls WHERE parent_issue_id=?
 UNION ALL SELECT c.issue_id FROM issue_controls c JOIN descendants d ON c.parent_issue_id=d.id)
 SELECT EXISTS(SELECT 1 FROM task_work_item_owners o JOIN descendants d ON d.id=o.work_item_id WHERE o.task_id=?)`, issueID, taskID).Scan(&descendant)
	return descendant, err
}
