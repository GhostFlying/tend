package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type sweepCapabilityWaits struct{}

type capabilityWait struct {
	taskID      string
	specVersion int64
	role        protocol.RunRole
}

// pendingCapabilityWaits is shared by the read-only due check and the write
// transaction. The latter rechecks notice identity after concurrent sweeps.
func pendingCapabilityWaits(ctx context.Context, q store.Querier, now int64) ([]capabilityWait, error) {
	rows, err := store.ListClaimCandidates(ctx, q)
	if err != nil {
		return nil, err
	}
	candidates, err := views.SchedulingCandidates(rows)
	if err != nil {
		return nil, err
	}
	var waits []capabilityWait
	seen := map[capabilityWait]bool{}
	for _, c := range candidates {
		if c.TargetSpecVersion != c.SpecVersion || c.Control != protocol.ControlManaged || c.Lifecycle.Terminal() || c.CreatedAt+600000 > now || c.AgentSelection == nil {
			continue
		}
		roles := []protocol.RunRole{protocol.RoleImplement}
		if c.Kind == protocol.ActionAccept {
			roles = []protocol.RunRole{protocol.RoleAccept}
		}
		for _, role := range roles {
			required := c.AgentSelection.Implementation.RequiredCapabilities
			if role == protocol.RoleAccept {
				required = c.AgentSelection.Acceptance.RequiredCapabilities
			}
			if len(required) == 0 {
				continue
			}
			var notified bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM capability_wait_notices WHERE task_id=? AND spec_version=? AND role=?)`, c.TaskID, c.TargetSpecVersion, role).Scan(&notified); err != nil {
				return nil, err
			}
			if !notified {
				wait := capabilityWait{taskID: c.TaskID, specVersion: c.TargetSpecVersion, role: role}
				if !seen[wait] {
					waits = append(waits, wait)
					seen[wait] = true
				}
			}
		}
	}
	return waits, nil
}

func (sweepCapabilityWaits) Operation() string { return "capabilities.sweep_waits" }
func (sweepCapabilityWaits) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (sweepCapabilityWaits) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	waits, err := pendingCapabilityWaits(ctx, tx.Q(), x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{NoEffect: true}
	for _, wait := range waits {
		explanation, err := views.TaskCapabilities(ctx, tx.Q(), wait.taskID, wait.specVersion, wait.role, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		if explanation.State == "eligible" {
			continue
		}
		issue, err := store.GetTaskIssueOwner(ctx, tx.Q(), wait.taskID)
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
		p := protocol.CreateAttentionPayload{Kind: "action", Title: fmt.Sprintf("Required capability is still waiting for role %s. Read task capability-explain --role %s; explicitly register the required declarations and restore ordinary availability of the same configured executor, or explicitly revise a future task. Reconcile unknown original requests first.", wait.role, wait.role), IssueID: issue}
		if issue != "" {
			p.TaskID = wait.taskID
		}
		inner := *x
		env := *x.Env
		inner.Env = &env
		env.Target = protocol.Target{Type: "attention", ID: protocol.NativeObjectID(protocol.Prefix("attn"), wait.taskID, fmt.Sprintf("capability-wait/%d/%s", wait.specVersion, wait.role))}
		env.ExpectedControlRevision = nil
		env.Payload, _ = json.Marshal(p)
		notice, err := createAttention(ctx, &inner, tx)
		if err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO capability_wait_notices(task_id,spec_version,role,request_id,created_at) VALUES(?,?,?,?,?)`, wait.taskID, wait.specVersion, wait.role, x.Env.RequestID, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		out.Events = append(out.Events, notice.Events...)
		out.Events = append(out.Events, Event{ObjectType: "task", ObjectID: wait.taskID, Kind: "task.capability_wait_notified", Body: map[string]any{"explanation": explanation}})
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
		// Cheap prefilter for ordinary tasks. A notice for one role must not
		// hide an outstanding notice for the other role. The shared predicate
		// below applies waiver and first-allocation boundaries precisely.
		now := a.now().UnixMilli()
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plan_actions a JOIN task_specs s ON s.task_id=a.task_id AND s.spec_version=a.target_spec_version WHERE a.status='pending' AND a.kind IN ('implement','accept') AND a.created_at<=? AND ((a.kind='implement' AND json_array_length(s.body_json,'$.agent_selection.implementation.required_capabilities')>0 AND NOT EXISTS(SELECT 1 FROM capability_wait_notices n WHERE n.task_id=a.task_id AND n.spec_version=a.target_spec_version AND n.role='implement')) OR (json_array_length(s.body_json,'$.agent_selection.acceptance.required_capabilities')>0 AND NOT EXISTS(SELECT 1 FROM capability_wait_notices n WHERE n.task_id=a.task_id AND n.spec_version=a.target_spec_version AND n.role='accept'))))`, now-600000).Scan(&due); err != nil || !due {
			return err
		}
		waits, err := pendingCapabilityWaits(ctx, tx.Q(), now)
		due = len(waits) > 0
		return err
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
