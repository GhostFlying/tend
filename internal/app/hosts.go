package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DefaultHostLostAfter is how long a host may go unheard before the
// workspaces on it count as unavailable (D62).
const DefaultHostLostAfter = 24 * time.Hour

// MinHostLostAfter is the shortest loss timeout that works: an idle
// runner is only heard from when it polls for work, which is recorded at
// most once a minute, and workspaces are checked once a minute.
const MinHostLostAfter = 10 * time.Minute

// WithHostLostAfter sets how long a host may go unheard before the
// workspaces on it count as unavailable; 0 never. A positive d should be
// at least MinHostLostAfter.
func WithHostLostAfter(d time.Duration) Option {
	return func(a *App) { a.hostLost = d }
}

// SeeHost records that a runner of the host is polling for work. An idle
// runner sends nothing else, so without this it would look lost.
func (a *App) SeeHost(ctx context.Context, hostID string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error { return tx.SeeHost(ctx, hostID, a.now().UnixMilli()) })
}

// CheckWorkspaces runs a workspace check as tend itself and returns the
// tasks it blocked or unblocked.
func (a *App) CheckWorkspaces(ctx context.Context) ([]string, error) {
	env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: CheckWorkspaces{}.Operation(),
		Target: protocol.Target{Type: "config"}, Payload: json.RawMessage("{}")}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, CheckWorkspaces{})
	if err != nil {
		return nil, err
	}
	if op.Status == protocol.OpFailed {
		return nil, op.Error
	}
	var res checkResult
	if err := json.Unmarshal(op.Result, &res); err != nil {
		return nil, fmt.Errorf("app: decode workspace check: %w", err)
	}
	return res.Tasks, nil
}

// checkResult is the result of workspaces.check.
type checkResult struct {
	Tasks []string `json:"tasks"`
}

// CheckWorkspaces handles workspaces.check (D62). A task whose workspace
// is on a host that is retired or has not been heard from for too long,
// and whose work waits on that host (an implementation or acceptance to
// claim, or a run there that tend lost track of), is blocked with a
// decision for the owner; once the host is back, the block is lifted and tend answers
// the decision itself. Finding nothing to change has no effect.
type CheckWorkspaces struct{}

// Operation implements Command.
func (CheckWorkspaces) Operation() string { return "workspaces.check" }

// Authorize implements Command.
func (CheckWorkspaces) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}

// Apply implements Command.
func (CheckWorkspaces) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	rows, err := store.ListWorkspaceHosts(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	var events []Event
	var changed []string
	for _, r := range rows {
		why := ""
		switch {
		case r.HostStatus != store.HostActive:
			why = "was retired"
		case x.HostLostAfter > 0 && r.LastSeenAt > 0 && r.LastSeenAt < now-x.HostLostAfter.Milliseconds():
			// A host never heard from gives nothing to go by. The time is
			// the server's local time, which is usually the owner's, with
			// its offset so it stays unambiguous otherwise.
			why = fmt.Sprintf("has not been heard from since %s", time.UnixMilli(r.LastSeenAt).Format("2006-01-02 15:04 -07:00"))
		}
		var d domain.Decision
		switch {
		case why != "" && r.Waiting && r.Block == protocol.BlockNone:
			row, spec, err := loadTask(ctx, tx.Q(), r.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			d = domain.WorkspaceUnavailable(toDomainWithWorkspace(row, spec), r.HostID, why)
		case why != "" && r.Block == protocol.BlockWorkspaceUnavailable:
			row, spec, err := loadTask(ctx, tx.Q(), r.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			asked, err := askedAboutWorkspace(ctx, tx.Q(), row)
			if err != nil {
				return Outcome{}, err
			}
			if asked {
				continue
			}
			d = domain.AskAboutWorkspaceAgain(toDomainWithWorkspace(row, spec), r.HostID, why)
		case why == "" && r.Block == protocol.BlockWorkspaceUnavailable:
			row, spec, err := loadTask(ctx, tx.Q(), r.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			d = domain.WorkspaceAvailable(toDomainWithWorkspace(row, spec), r.HostID)
			answered, err := answerForHost(ctx, x, tx, r.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			events = append(events, answered...)
		default:
			continue
		}
		if len(d.Events) == 0 {
			continue
		}
		row, err := store.GetTask(ctx, tx.Q(), r.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		_, evs, err := applyDecision(ctx, x, tx, row, d, nil)
		if err != nil {
			return Outcome{}, err
		}
		events = append(events, evs...)
		changed = append(changed, r.TaskID)
	}
	if len(changed) == 0 {
		return Outcome{NoEffect: true}, nil
	}
	return Outcome{Result: checkResult{Tasks: changed}, Events: events}, nil
}

// askedAboutWorkspace reports whether the owner still has, or has
// answered, a question about the task's unavailable workspace that can
// take effect: one still open, or one asked at the task's current control
// revision. Otherwise, after a rating or a pause, starting over would be
// out of reach.
func askedAboutWorkspace(ctx context.Context, q store.Querier, task store.TaskRow) (bool, error) {
	decs, err := store.ListDecisions(ctx, q, task.TaskID)
	if err != nil {
		return false, err
	}
	for _, d := range decs {
		if isWorkspaceDecision(d) && (d.Status == store.DecisionOpen || d.AskedRevision.Int64 == task.ControlRevision) {
			return true, nil
		}
	}
	return false, nil
}

func isWorkspaceDecision(d store.DecisionRow) bool {
	var effects map[string]protocol.DecisionEffect
	_ = json.Unmarshal([]byte(d.EffectsJSON), &effects)
	if effects[domain.StartOverOption] == protocol.EffectStartOver {
		return true
	}
	var options []string
	_ = json.Unmarshal([]byte(d.OptionsJSON), &options)
	if !slices.Contains(options, domain.WaitForHostOption) {
		return false
	}
	for _, effect := range effects {
		if effect == protocol.EffectCancel {
			return true
		}
	}
	return false
}

// answerForHost answers, for the owner, the open decisions about a task's
// unavailable workspace once its host is back: waiting was all that was
// left to do.
func answerForHost(ctx context.Context, x *Exec, tx *store.Tx, taskID string) ([]Event, error) {
	decs, err := store.ListDecisions(ctx, tx.Q(), taskID)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, d := range decs {
		if d.Status != store.DecisionOpen || !isWorkspaceDecision(d) {
			continue
		}
		answer, err := json.Marshal(decisionAnswer{Option: domain.WaitForHostOption, Text: "The host is back, so tend answered for you."})
		if err != nil {
			return nil, fmt.Errorf("app: encode answer: %w", err)
		}
		if err := tx.AnswerDecision(ctx, d.DecisionID, d.Version, string(answer), "", x.Now.UnixMilli()); err != nil {
			return nil, err
		}
		events = append(events, Event{ObjectType: "decision", ObjectID: d.DecisionID, ObjectVersion: d.Version + 1, Kind: "decision.answered",
			Body: map[string]any{"task_id": taskID, "answer": json.RawMessage(answer)}})
	}
	return events, nil
}
