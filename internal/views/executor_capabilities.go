package views

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func currentCapabilityEvidence(ctx context.Context, q store.Querier, pin protocol.RunExecutionPin, r protocol.RunnerRuntime) ([]protocol.ExecutorCapabilityEvidence, error) {
	all, err := store.AgentCapabilityEvidence(ctx, q, pin.Identity.ExecutionEnvironmentID, pin.Identity.LocalID)
	if err != nil {
		return nil, err
	}
	out := []protocol.ExecutorCapabilityEvidence{}
	for _, e := range all {
		if e.Pin == pin && e.InstanceID == r.InstanceID && e.Generation == r.Generation {
			out = append(out, e)
		}
	}
	return out, nil
}

// CapabilityAgents retains declarations and historical evidence even when the
// corresponding adapter is offline. It never probes or creates reservations.
func CapabilityAgents(ctx context.Context, q store.Querier, now int64) ([]protocol.AgentCapabilityView, string, error) {
	return CapabilityAgentsForHost(ctx, q, "", now)
}
func CapabilityAgentsForHost(ctx context.Context, q store.Querier, host string, now int64) ([]protocol.AgentCapabilityView, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT projection_json FROM agent_inventory_versions v WHERE (?='' OR v.host_id=?) AND server_revision=(SELECT MAX(n.server_revision) FROM agent_inventory_versions n WHERE n.host_id=v.host_id) ORDER BY host_id`, host, host)
	if err != nil {
		return nil, "", err
	}
	inventories := []protocol.AgentInventoryProjection{}
	for rows.Next() {
		var body string
		var inv protocol.AgentInventoryProjection
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return nil, "", err
		}
		if err := json.Unmarshal([]byte(body), &inv); err != nil {
			rows.Close()
			return nil, "", err
		}
		inventories = append(inventories, inv)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", err
	}
	rows.Close()
	out := []protocol.AgentCapabilityView{}
	// The signature rejects continuations of a listing after mutable runtime
	// facts change. A stale cursor is explicit; it cannot silently skip results.
	var signature []any
	for _, inv := range inventories {
		runtime, err := store.GetHostRuntime(ctx, q, inv.ExecutionEnvironmentID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, "", err
		}
		signature = append(signature, inv, runtime)
		executors, err := HostExecutors(ctx, q, inv.ExecutionEnvironmentID, now)
		if err != nil {
			return nil, "", err
		}
		for _, agent := range inv.Agents {
			evidence, err := store.AgentCapabilityEvidence(ctx, q, inv.ExecutionEnvironmentID, agent.Identity.LocalID)
			if err != nil {
				return nil, "", err
			}
			signature = append(signature, evidence)
			v := protocol.AgentCapabilityView{Binding: agent.Binding, Categories: slices.Clone(agent.Categories), Purposes: slices.Clone(agent.Purposes), Identity: agent.Identity, AgentRevision: agent.Revision, Declared: slices.Clone(agent.Capabilities), Evidence: evidence, Current: protocol.CapabilityMatch{State: "pending", Required: slices.Clone(agent.Capabilities), Reasons: []protocol.CapabilityReason{}, EvidenceIDs: []string{}, CheckedAtMillis: now}}
			found := false
			for _, executor := range executors {
				if executor.Pin.Identity != agent.Identity || executor.Pin.Purpose != "" {
					continue
				}
				p := executor.Pin
				v.Pin = &p
				v.Current = scheduler.ExecutorCapabilityMatch(executor, agent.Capabilities)
				found = true
				break
			}
			if len(agent.Capabilities) == 0 {
				v.Current.State = "pending"
				v.Current.Reasons = []protocol.CapabilityReason{{State: "pending", Reason: "no_declaration", Recovery: "declare and register capabilities explicitly; category and model names grant none"}}
			} else if !found {
				v.Current.State = "pending"
				for _, ref := range agent.Capabilities {
					v.Current.Reasons = append(v.Current.Reasons, protocol.CapabilityReason{Capability: ref, State: "pending", Reason: "runner_or_adapter_unavailable", Recovery: "restore the same registered executor and obtain fresh evidence"})
				}
			}
			out = append(out, v)
		}
	}
	body, err := json.Marshal(signature)
	return out, string(body), err
}

// TaskCapabilities uses the scheduler's candidates, dependency facts, shared
// admission and SelectExecution. Historical specs are evaluated as historical
// requirements, never rewritten to the latest definition or Agent revision.
func TaskCapabilities(ctx context.Context, q store.Querier, taskID string, version int64, role protocol.RunRole, now int64) (protocol.TaskCapabilityExplanation, error) {
	task, err := store.GetTask(ctx, q, taskID)
	if err != nil {
		return protocol.TaskCapabilityExplanation{}, err
	}
	if version == 0 {
		version = task.SpecVersion
	}
	body, err := store.GetSpec(ctx, q, taskID, version)
	if err != nil {
		return protocol.TaskCapabilityExplanation{}, err
	}
	var spec domain.Spec
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		return protocol.TaskCapabilityExplanation{}, err
	}
	refs := []protocol.CapabilityReference{}
	if spec.AgentSelection != nil {
		refs = spec.AgentSelection.Implementation.RequiredCapabilities
		if role == protocol.RoleAccept {
			refs = spec.AgentSelection.Acceptance.RequiredCapabilities
		}
	}
	v := protocol.TaskCapabilityExplanation{TaskID: taskID, SpecVersion: version, Role: role, Required: slices.Clone(refs), State: "unavailable", Candidates: []protocol.AgentCapabilityView{}, Reasons: []string{}, CheckedAtMillis: now, DeadlineMillis: task.CreatedAt + 600000, Recovery: "read exact candidate reasons; verify the same entry once or explicitly revise a future Task spec; reconcile unknown original requests"}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return v, err
	}
	c, ok := facts.candidates[taskID]
	if !ok || version != task.SpecVersion {
		v.Reasons = append(v.Reasons, "no_current_schedulable_action; historical requirements remain frozen")
		return v, nil
	}
	if role == protocol.RoleAccept {
		c.Kind = protocol.ActionAccept
	} else {
		c.Kind = protocol.ActionImplement
	}
	c.AgentSelection = spec.AgentSelection
	v.DeadlineMillis = c.CreatedAt + 600000
	for _, host := range facts.hosts {
		// Re-read with the injected clock; generic execution projections also
		// serve historical state and intentionally omit a wall clock.
		host.Executors, err = HostExecutors(ctx, q, host.ID, now)
		if err != nil {
			return v, err
		}
		host.Continuations, err = HostContinuations(ctx, q, host.ID, now)
		if err != nil {
			return v, err
		}
		if !host.available {
			v.Reasons = append(v.Reasons, "runner_unavailable:"+host.ID)
			if v.State != "eligible" {
				v.State = "pending"
			}
			continue
		}
		// Admission conditions not related to capabilities are shared too.
		host.ConcurrencyBlocked = map[string]bool{}
		runtime, err := store.GetHostRuntime(ctx, q, host.ID)
		if err != nil {
			return v, err
		}
		for _, e := range append(slices.Clone(host.Executors), host.Continuations...) {
			available, err := RuntimeConcurrencyAvailable(ctx, q, host.ID, e.Pin.Runtime.RuntimeID, "", now)
			if err != nil {
				return v, err
			}
			host.ConcurrencyBlocked[e.Pin.Runtime.RuntimeID] = !available
		}
		if !runtime.Online || now-runtime.LastHeartbeatAt >= 60000 {
			v.Reasons = append(v.Reasons, "runner_offline:"+host.ID)
			if v.State != "eligible" {
				v.State = "pending"
			}
			continue
		}
		reason := scheduler.Skip(host.Host, c)
		eligible := reason == ""
		if !eligible {
			v.Reasons = append(v.Reasons, reason)
		}
		for _, executor := range append(slices.Clone(host.Executors), host.Continuations...) {
			p := executor.Pin
			if p.Purpose != "" {
				continue
			}
			m := scheduler.ExecutorCapabilityMatch(executor, refs)
			trial := c
			if c.AgentSelection != nil {
				selection := c.AgentSelection.Clone()
				if role == protocol.RoleAccept {
					selection.Acceptance.AgentID = p.Identity.ExecutionEnvironmentID + "/" + p.Identity.LocalID
					selection.Acceptance.RuntimeID = p.Runtime.RuntimeID
				} else {
					selection.Implementation.AgentID = p.Identity.ExecutionEnvironmentID + "/" + p.Identity.LocalID
					selection.Implementation.RuntimeID = p.Runtime.RuntimeID
				}
				trial.AgentSelection = &selection
				trial.WorkspaceBackendID = p.BackendID
			}
			schedulingReason := scheduler.SkipReason(host.Host, trial)
			candidate := protocol.AgentCapabilityView{SchedulingReason: schedulingReason, Binding: p.Runtime, Categories: slices.Clone(executor.Categories), Purposes: []string{"worker_run"}, Identity: p.Identity, AgentRevision: p.AgentRevision, Declared: slices.Clone(executor.DeclaredCapabilities), Pin: &p, Current: m, Evidence: executor.CapabilityEvidence}
			v.Candidates = append(v.Candidates, candidate)
			if m.State == "pending" && v.State != "eligible" {
				v.State = "pending"
			}
		}
		if eligible {
			v.State = "eligible"
		}
	}
	if len(refs) == 0 && spec.AgentSelection == nil {
		v.State = "eligible"
		v.Reasons = append(v.Reasons, "no_capability_constraints; existing legacy scheduling still applies")
	}
	slices.Sort(v.Reasons)
	v.Reasons = slices.Compact(v.Reasons)
	return v, nil
}

func CapabilityAgentKey(v protocol.AgentCapabilityView) string {
	return v.Identity.ExecutionEnvironmentID + "/" + v.Identity.LocalID
}

func FilterCapabilityAgents(items []protocol.AgentCapabilityView, f protocol.AgentCapabilityQuery) []protocol.AgentCapabilityView {
	return slices.DeleteFunc(items, func(v protocol.AgentCapabilityView) bool {
		if f.Environment != "" && f.Environment != v.Identity.ExecutionEnvironmentID || f.Agent != "" && f.Agent != v.Identity.LocalID || f.Runtime != "" && v.Binding.RuntimeID != f.Runtime || f.State != "" && v.Current.State != f.State {
			return true
		}
		if f.Category != "" && !slices.Contains(v.Categories, f.Category) || f.Purpose != "" && !slices.Contains(v.Purposes, f.Purpose) {
			return true
		}
		if f.Capability != "" && !slices.ContainsFunc(v.Declared, func(r protocol.CapabilityReference) bool { return r.CapabilityID == f.Capability }) {
			return true
		}
		return false
	})
}

func ValidateCapabilityState(state string) bool {
	return state == "" || slices.Contains(strings.Fields("eligible pending unavailable"), state)
}
