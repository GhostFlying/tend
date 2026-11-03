package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerPinForManager uses the same accepted snapshot as the continuing owner.
// The backend is host-local bootstrap data: only an unambiguous observed backend
// is usable when the snapshot's runtime does not determine it itself.
func WorkerPinForManager(ctx context.Context, q Querier, manager protocol.RunExecutionPin) (protocol.RunExecutionPin, error) {
	inv, err := GetAgentInventoryAt(ctx, q, manager)
	if err != nil {
		return protocol.RunExecutionPin{}, err
	}
	for _, a := range inv.Projection.Agents {
		if a.Identity != manager.Identity || a.Revision != manager.AgentRevision || !a.Enabled || !slices.Contains(a.Purposes, "worker_run") {
			continue
		}
		p := manager
		p.Purpose = ""
		p.Runtime = a.Binding
		p.Profile = protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}
		for _, r := range inv.Projection.Runtimes {
			if r.RuntimeID == a.Binding.RuntimeID && r.Revision == a.Binding.Revision && r.Enabled {
				if r.Kind == "orca-worker" || r.Kind == "central-traex-worker" {
					p.Worker = protocol.WorkerOrca
					p.BackendID = "orca"
				} else if r.Kind == "appserver-worker" {
					p.Worker = protocol.WorkerAppServer
					runtime, err := GetHostRuntime(ctx, q, p.Identity.ExecutionEnvironmentID)
					if err != nil {
						return p, err
					}
					backends := map[string]bool{}
					if slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentContinuation) && (runtime.Runtime.ContinuationBackend == "git" || runtime.Runtime.ContinuationBackend == "orca") {
						backends[runtime.Runtime.ContinuationBackend] = true
					}
					for _, e := range append(slices.Clone(runtime.Runtime.AgentExecutors), runtime.Runtime.AgentContinuations...) {
						if e.Pin.Purpose == "" && (e.Pin.BackendID == "git" || e.Pin.BackendID == "orca") {
							backends[e.Pin.BackendID] = true
						}
					}
					if len(backends) != 1 {
						return p, ErrNotFound
					}
					for backend := range backends {
						p.BackendID = backend
					}
				} else {
					continue
				}
				if p.Validate(p.Identity.ExecutionEnvironmentID) == nil {
					return p, nil
				}
			}
		}
	}
	return protocol.RunExecutionPin{}, ErrNotFound
}

// RequiredAgentExecutions derives retention from durable responsibility and run
// references, never from the runner's cache or an old enabled flag alone.
func RequiredAgentExecutions(ctx context.Context, q Querier, host string) ([]protocol.RunExecutionPin, error) {
	raw, err := FollowupIDs(ctx, q, `SELECT m.pin_json FROM issue_manager_tasks m JOIN issue_controls i ON i.issue_id=m.issue_id WHERE m.host_id=? AND m.pin_json IS NOT NULL AND ((m.state='assigned' AND m.assignment_epoch=i.assignment_epoch AND i.assignment_state='active' AND i.state IN ('active','paused','cancelling')) OR EXISTS (SELECT 1 FROM plan_actions a WHERE a.task_id=m.task_id AND a.status='pending' AND json_extract(a.trigger_json,'$.receipt_only')=1))
 UNION SELECT e.pin_json FROM run_agent_executions e JOIN runs r USING(run_id) WHERE r.host_id=? AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')
 UNION SELECT e.pin_json FROM run_agent_executions e JOIN runs r USING(run_id) JOIN run_question_handoffs h USING(run_id) JOIN decisions d USING(decision_id) JOIN tasks t ON t.task_id=r.task_id JOIN plan_actions a ON a.action_id=r.action_id WHERE r.host_id=? AND h.stopped=1 AND h.successor_run_id IS NULL AND d.status IN ('open','answered') AND t.lifecycle NOT IN ('completed','cancelled','failed') AND a.target_spec_version=t.spec_version UNION SELECT e.pin_json FROM run_agent_executions e JOIN run_issue_responsibilities s USING(run_id) JOIN issue_controls i USING(issue_id) JOIN runs r USING(run_id) JOIN tasks t USING(task_id) WHERE r.host_id=? AND s.assignment_epoch=i.assignment_epoch AND s.agent_ref=i.assignee_ref AND i.assignment_state='active' AND i.state IN ('active','paused','cancelling') AND t.lifecycle NOT IN ('completed','cancelled','failed')`, host, host, host, host)
	if err != nil {
		return nil, err
	}
	tasks, err := FollowupIDs(ctx, q, `SELECT c.task_id FROM task_execution_contracts c JOIN issue_controls i ON i.issue_id=c.issue_id JOIN tasks t USING(task_id) WHERE c.agent_ref LIKE ? ESCAPE '\' AND c.assignment_epoch=i.assignment_epoch AND c.agent_ref=i.assignee_ref AND i.assignment_state='active' AND i.state IN ('active','paused','cancelling') AND t.lifecycle NOT IN ('completed','cancelled','failed')`, strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(host)+"/%")
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		c, err := GetTaskOwnerContract(ctx, q, task)
		if err != nil {
			return nil, err
		}
		if c.Execution != nil {
			b, err := json.Marshal(c.Execution)
			if err != nil {
				return nil, err
			}
			raw = append(raw, string(b))
		}
	}
	seen := map[protocol.RunExecutionPin]bool{}
	out := []protocol.RunExecutionPin{}
	for _, body := range raw {
		var p protocol.RunExecutionPin
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, err
		}
		if p.Validate(host) != nil || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if p.Purpose == "issue_manager" {
			worker, err := WorkerPinForManager(ctx, q, p)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			if err == nil && !seen[worker] {
				seen[worker] = true
				out = append(out, worker)
			}
		}
	}
	slices.SortFunc(out, func(a, b protocol.RunExecutionPin) int {
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		return strings.Compare(string(aa), string(bb))
	})
	return out, nil
}

// RetainedOwnerInventory resolves the exact responsibility revision, including
// when the current registration has removed or disabled this Agent.
func RetainedOwnerInventory(ctx context.Context, q Querier, issue protocol.Issue) (AgentInventoryRow, *protocol.RunExecutionPin, error) {
	m, err := CurrentIssueManagerTask(ctx, q, issue.IssueID)
	if err == nil && m.Pin != nil && m.AssignmentEpoch == issue.AssignmentEpoch && m.Pin.Identity.ExecutionEnvironmentID+"/"+m.Pin.Identity.LocalID == issue.Assignee.Ref {
		runtime, e := GetHostRuntime(ctx, q, m.Pin.Identity.ExecutionEnvironmentID)
		if e != nil && !errors.Is(e, ErrNotFound) {
			return AgentInventoryRow{}, nil, e
		}
		if e == nil && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentContinuation) {
			inv, err := GetAgentInventoryAt(ctx, q, *m.Pin)
			return inv, m.Pin, err
		}
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return AgentInventoryRow{}, nil, err
	}
	parts := strings.Split(issue.Assignee.Ref, "/")
	if len(parts) != 2 {
		return AgentInventoryRow{}, nil, ErrNotFound
	}
	inv, err := GetAgentInventory(ctx, q, parts[0])
	return inv, nil, err
}
