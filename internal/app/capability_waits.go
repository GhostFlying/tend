package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type sweepCapabilityWaits struct{}

func (sweepCapabilityWaits) Operation() string { return "capabilities.sweep_waits" }
func (sweepCapabilityWaits) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (sweepCapabilityWaits) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	rows, err := store.ListClaimCandidates(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	candidates, err := views.SchedulingCandidates(rows)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{NoEffect: true}
	for _, c := range candidates {
		if c.TargetSpecVersion != c.SpecVersion || c.Control != protocol.ControlManaged || c.Lifecycle.Terminal() || len(scheduler.RequiredCapabilities(c)) == 0 || c.CreatedAt+600000 > x.Now.UnixMilli() {
			continue
		}
		role := protocol.RoleImplement
		if c.Kind == protocol.ActionAccept {
			role = protocol.RoleAccept
		}
		var notified bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM capability_wait_notices WHERE task_id=? AND spec_version=? AND role=?)`, c.TaskID, c.TargetSpecVersion, role).Scan(&notified); err != nil {
			return Outcome{}, err
		}
		if notified {
			continue
		}
		explanation, err := views.TaskCapabilities(ctx, tx.Q(), c.TaskID, c.TargetSpecVersion, role, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		if explanation.State == "eligible" {
			continue
		}
		issue, err := store.GetTaskIssueOwner(ctx, tx.Q(), c.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if issue != "" {
			current, err := store.GetCanonicalIssue(ctx, tx.Q(), issue)
			if errors.Is(err, store.ErrNotFound) {
				issue = ""
			} else if err != nil {
				return Outcome{}, err
			} else if current.State != protocol.IssueActive {
				continue
			}
		}
		p := protocol.CreateAttentionPayload{Kind: "action", Title: "Required capability is still waiting. Read task capability-explain; explicitly register the required declarations and restore ordinary availability of the same configured executor, or explicitly revise a future task. Reconcile unknown original requests first.", IssueID: issue}
		if issue != "" {
			p.TaskID = c.TaskID
		}
		inner := *x
		env := *x.Env
		inner.Env = &env
		env.Target = protocol.Target{Type: "attention"}
		env.ExpectedControlRevision = nil
		env.Payload, _ = json.Marshal(p)
		notice, err := createAttention(ctx, &inner, tx)
		if err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO capability_wait_notices(task_id,spec_version,role,request_id,created_at) VALUES(?,?,?,?,?)`, c.TaskID, c.TargetSpecVersion, role, x.Env.RequestID, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		out.Events = append(out.Events, notice.Events...)
		out.Events = append(out.Events, Event{ObjectType: "task", ObjectID: c.TaskID, Kind: "task.capability_wait_notified", Body: map[string]any{"explanation": explanation}})
		out.NoEffect = false
		out.Notify = true
	}
	return out, nil
}

// SweepCapabilityWaits reuses the runner maintenance sweep. There is no timer,
// model call, verification retry or native session per waiting Task.
func (a *App) SweepCapabilityWaits(ctx context.Context) error {
	var due bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plan_actions a JOIN task_specs s ON s.task_id=a.task_id AND s.spec_version=a.target_spec_version WHERE a.status='pending' AND a.created_at<=? AND (json_array_length(s.body_json,'$.agent_selection.implementation.required_capabilities')>0 OR json_array_length(s.body_json,'$.agent_selection.acceptance.required_capabilities')>0) AND NOT EXISTS(SELECT 1 FROM capability_wait_notices n WHERE n.task_id=a.task_id AND n.spec_version=a.target_spec_version AND n.role=CASE WHEN a.kind='accept' THEN 'accept' ELSE 'implement' END))`, a.now().UnixMilli()-600000).Scan(&due)
	})
	if err != nil || !due {
		return err
	}
	cmd := sweepCapabilityWaits{}
	env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: protocol.Target{Type: "capability"}, Payload: json.RawMessage(`{}`)}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, cmd)
	if err == nil && op.Error != nil {
		return op.Error
	}
	return err
}
