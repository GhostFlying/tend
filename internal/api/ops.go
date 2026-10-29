package api

import (
	"context"
	"net/http"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// ownerCommands are the writes the owner API accepts, by operation. Work
// is created and planned by the agent the owner works in; the owner's own
// writes answer it, retry after a decision and steer tasks.
var ownerCommands = commandMap(
	app.IssueFollowupCommand{Action: "create"}, app.IssueFollowupCommand{Action: "update"}, app.IssueFollowupCommand{Action: "pause"}, app.IssueFollowupCommand{Action: "resume"}, app.IssueFollowupCommand{Action: "cancel"}, app.IssueFollowupCommand{Action: "trigger"}, app.IssueRefreshDelivery{},
	app.SendRunMessage{}, app.InjectSource{}, app.CreateConsultation{}, app.PromoteConsultation{},
	app.IssueDeadlineCommand{}, app.IssueDeadlineCommand{Cancel: true}, app.PutPlannerEntry{},
	app.AttentionCommand{Action: "create"}, app.AttentionCommand{Action: "update"}, app.AttentionCommand{Action: "respond"}, app.AttentionCommand{Action: "confirm"}, app.AttentionCommand{Action: "withdraw"}, app.AttentionCommand{Action: "present"},
	app.CanonicalOwnerIssueCommand{Action: "create"}, app.CanonicalOwnerIssueCommand{Action: "update"},
	app.CanonicalOwnerIssueCommand{Action: "pause"}, app.CanonicalOwnerIssueCommand{Action: "resume"},
	app.CanonicalOwnerIssueCommand{Action: "cancel"}, app.CanonicalOwnerIssueCommand{Action: "finish_cancel"},
	app.CanonicalOwnerIssueCommand{Action: "close"}, app.CanonicalOwnerIssueCommand{Action: "reopen"},
	app.CanonicalOwnerIssueCommand{Action: "reparent"}, app.CanonicalOwnerIssueCommand{Action: "dependencies"},
	app.CanonicalOwnerIssueCommand{Action: "review_sources"}, app.CanonicalOwnerIssueCommand{Action: "review_completions"},
	app.CanonicalOwnerTaskCreate{}, app.CanonicalOwnerTaskUpdate{}, app.HostedIssueAssignment{}, app.HostedIssueAssignment{Finish: true}, app.PutAgentCategory{}, app.RegisterRepository{}, app.SetIssueMessageAssociations{},
	app.WithdrawMessage{},
	app.UpdateWorkItemChoice{Action: "respond"},
	app.HostMaintenance{},
	app.ControlUpgrade{},
	app.AnswerDecision{},
	app.CreateAction{},
	app.SetControlMode{},
	app.RateTask{},
	app.CancelTask{},
)

// hostedCommands are the conversation writes, accepted only when the
// server runs the hosted main thread (D45).
var hostedCommands = commandMap(app.ResumeLogicalTurn{}, app.CompactConversation{}, app.SubmitMessage{}, app.InterruptReply{}, app.SetConversationHandoffSettings{}, app.ConversationHandoff{})

func commandMap(cmds ...app.Command) map[string]app.Command {
	m := make(map[string]app.Command, len(cmds))
	for _, c := range cmds {
		m[c.Operation()] = c
	}
	return m
}

// ops executes one owner write. A stale write about a task also returns
// the task's current state, so the caller can decide again.
func (s *Server) ops(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var env protocol.Envelope
	if err := decodeBodyLimit(w, r, &env, 28<<20); err != nil {
		s.writeError(w, r, err)
		return
	}
	if env.Operation != "conversation.submit_message" && len(env.Payload) > maxBody {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "body is too large"))
		return
	}
	cmd, ok := ownerCommands[env.Operation]
	if env.Operation == (app.SetConversationSettings{}).Operation() {
		if !s.cfg.HostedMainAgent {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "conversation settings need the hosted main agent"))
			return
		}
		view := s.settings(r.Context())
		cmd, ok = app.SetConversationSettings{Available: view.Available, Models: view.Models}, true
	}
	if hosted, isHosted := hostedCommands[env.Operation]; isHosted {
		if !s.cfg.HostedMainAgent {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "operation %q needs the hosted main agent, which this server does not run", env.Operation))
			return
		}
		cmd, ok = hosted, true
	}
	if !ok {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "operation %q is not available to owners", env.Operation))
		return
	}
	op, err := s.app.Execute(r.Context(), p, &env, cmd)
	if err != nil {
		s.writeError(w, r, err, "request_id", env.RequestID, "operation", env.Operation)
		return
	}
	res := protocol.APIResponse{Operation: &op}
	if op.Status == protocol.OpFailed && op.Error != nil && op.Error.Code == protocol.CodeStaleRevision && env.Target.Type == "issue" {
		_ = s.store.Read(r.Context(), func(tx *store.ReadTx) error {
			v, err := views.CanonicalIssue(r.Context(), tx.Q(), env.Target.ID, 0)
			if err == nil {
				res.CurrentIssue = &v
			}
			return err
		})
	}
	if op.Status == protocol.OpFailed && op.Error != nil && op.Error.Code == protocol.CodeStaleRevision && env.Target.Type == "task" {
		res.Current = s.currentTask(r.Context(), env.Target.ID)
	}
	if op.Status == protocol.OpFailed && op.Error != nil && op.Error.Code == protocol.CodeStaleRevision && env.Target.Type == "attention" {
		_ = s.store.Read(r.Context(), func(tx *store.ReadTx) error {
			v, err := views.Attention(r.Context(), tx.Q(), env.Target.ID)
			if err == nil {
				res.CurrentAttention = &v
			}
			return err
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) currentTask(ctx context.Context, taskID string) *protocol.TaskSummary {
	var out *protocol.TaskSummary
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetTask(ctx, tx.Q(), taskID)
		if err == nil {
			v, readErr := views.SummaryWithExecution(ctx, tx.Q(), row)
			if readErr != nil {
				return readErr
			}
			out = &v
		}
		return err
	}); err != nil {
		s.log.WarnContext(ctx, "could not read the current task", "task", taskID, "err", err)
	}
	return out
}
