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
	app.AnswerDecision{},
	app.CreateAction{},
	app.SetControlMode{},
	app.RateTask{},
	app.CancelTask{},
)

// hostedCommands are the conversation writes, accepted only when the
// server runs the hosted main thread (D45).
var hostedCommands = commandMap(app.SubmitMessage{}, app.InterruptReply{})

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
	if err := decodeBody(w, r, &env); err != nil {
		s.writeError(w, r, err)
		return
	}
	cmd, ok := ownerCommands[env.Operation]
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
	if op.Status == protocol.OpFailed && op.Error != nil && op.Error.Code == protocol.CodeStaleRevision && env.Target.Type == "task" {
		res.Current = s.currentTask(r.Context(), env.Target.ID)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) currentTask(ctx context.Context, taskID string) *protocol.TaskSummary {
	var out *protocol.TaskSummary
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetTask(ctx, tx.Q(), taskID)
		if err == nil {
			v := views.Summary(row)
			out = &v
		}
		return err
	}); err != nil {
		s.log.WarnContext(ctx, "could not read the current task", "task", taskID, "err", err)
	}
	return out
}
