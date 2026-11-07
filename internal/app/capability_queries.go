package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func capabilityPageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100")
	}
	return limit, nil
}

func (a *App) CapabilityDefinitions(ctx context.Context, id, state, search, after string, limit int) (protocol.CapabilityPage, error) {
	if id != "" && !protocol.AgentInventoryID(id) {
		return protocol.CapabilityPage{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid capability identity")
	}
	if state == "" {
		state = "active"
		if id != "" {
			state = "all"
		}
	}
	var page protocol.CapabilityPage
	if !slices.Contains([]string{"active", "retired", "all"}, state) {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "state must be active, retired or all")
	}
	limit, err := capabilityPageLimit(limit)
	if err != nil {
		return page, err
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		page, err = store.ListCapabilityDefinitions(ctx, tx.Q(), id, state, search, after, limit)
		return err
	})
	return page, err
}

func (a *App) CapabilityAgents(ctx context.Context, f protocol.AgentCapabilityQuery) (protocol.AgentCapabilityPage, error) {
	page := protocol.AgentCapabilityPage{Items: []protocol.AgentCapabilityView{}}
	if f.Agent != "" && f.Environment == "" || !views.ValidateCapabilityState(f.State) {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "agent requires environment; state must be eligible, pending or unavailable")
	}
	limit, err := capabilityPageLimit(f.Limit)
	if err != nil {
		return page, err
	}
	filter := f
	filter.After = ""
	filter.Limit = 0
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		now := a.now().UnixMilli()
		// Decode the original checked-at before deriving heartbeat dependent states.
		clockCursor, err := store.CapabilityCursor(filter, f.After, now)
		if err != nil {
			return err
		}
		items, signature, err := views.CapabilityAgents(ctx, tx.Q(), clockCursor.AsOf)
		if err != nil {
			return err
		}
		// A second opaque cursor seals both filters and the mutable source facts.
		// Its key is embedded in the outer cursor, which stays independent of time.
		keyCursor, err := store.CapabilityCursor([]any{filter, signature}, clockCursor.After, now)
		if err != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "capability listing changed; start a fresh page")
		}
		page.AsOf = clockCursor.AsOf
		items = views.FilterCapabilityAgents(items, f)
		for _, item := range items {
			key := views.CapabilityAgentKey(item)
			if key <= keyCursor.After {
				continue
			}
			if len(page.Items) == limit {
				page.Next = clockCursor.Encode(keyCursor.Encode(views.CapabilityAgentKey(page.Items[len(page.Items)-1])))
				break
			}
			page.Items = append(page.Items, item)
		}
		return nil
	})
	return page, err
}

func (a *App) TaskCapabilityExplanation(ctx context.Context, task string, version int64, role protocol.RunRole) (protocol.TaskCapabilityExplanation, error) {
	return a.TaskCapabilityExplanationPage(ctx, task, version, role, protocol.AgentCapabilityQuery{})
}
func (a *App) TaskCapabilityExplanationPage(ctx context.Context, task string, version int64, role protocol.RunRole, f protocol.AgentCapabilityQuery) (protocol.TaskCapabilityExplanation, error) {
	var p protocol.TaskCapabilityExplanation
	if role == "" {
		role = protocol.RoleImplement
	}
	if role != protocol.RoleImplement && role != protocol.RoleAccept || version < 0 || !views.ValidateCapabilityState(f.State) || f.Agent != "" && f.Environment == "" {
		return p, protocol.Errorf(protocol.CodeInvalidRequest, "invalid capability role, spec_version or candidate filters")
	}
	limit, err := capabilityPageLimit(f.Limit)
	if err != nil {
		return p, err
	}
	filter := f
	filter.After = ""
	filter.Limit = 0
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		now := a.now().UnixMilli()
		clockCursor, err := store.CapabilityCursor([]any{task, version, role, filter}, f.After, now)
		if err != nil {
			return err
		}
		p, err = views.TaskCapabilities(ctx, tx.Q(), task, version, role, clockCursor.AsOf)
		if err != nil {
			return err
		}
		signature, err := json.Marshal(p)
		if err != nil {
			return err
		}
		keyCursor, err := store.CapabilityCursor([]any{"task-candidate-pins-v2", string(signature)}, clockCursor.After, now)
		if err != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "capability match facts changed; start a fresh page")
		}
		all := views.FilterCapabilityAgents(p.Candidates, f)
		p.Candidates = []protocol.AgentCapabilityView{}
		p.AsOf = clockCursor.AsOf
		slices.SortFunc(all, func(a, b protocol.AgentCapabilityView) int {
			return strings.Compare(capabilityCandidateKey(a), capabilityCandidateKey(b))
		})
		for _, v := range all {
			key := capabilityCandidateKey(v)
			if key <= keyCursor.After {
				continue
			}
			if len(p.Candidates) == limit {
				p.Next = clockCursor.Encode(keyCursor.Encode(capabilityCandidateKey(p.Candidates[len(p.Candidates)-1])))
				break
			}
			p.Candidates = append(p.Candidates, v)
		}
		return nil
	})
	return p, err
}
func capabilityCandidateKey(v protocol.AgentCapabilityView) string {
	// Current and retained executions may share Agent/runtime/backend while
	// differing in revision, snapshot or profile. Seal the entire frozen pin
	// into the ordering key so an after boundary cannot skip a distinct pin.
	// RunExecutionPin contains only scalar fields, so marshaling cannot fail.
	pin, _ := json.Marshal(v.Pin)
	return views.CapabilityAgentKey(v) + "/" + string(pin)
}

func (a *App) CapabilityAgentRevisions(ctx context.Context, environment, agent string, revision int64, after string, limit int) (protocol.AgentCapabilityRevisionPage, error) {
	var page protocol.AgentCapabilityRevisionPage
	if !protocol.AgentInventoryID(environment) || !protocol.AgentInventoryID(agent) || revision < 0 {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "valid environment, Agent and nonnegative revision are required")
	}
	limit, err := capabilityPageLimit(limit)
	if err != nil {
		return page, err
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		page, err = store.ListAgentCapabilityRevisions(ctx, tx.Q(), environment, agent, revision, after, limit)
		return err
	})
	return page, err
}
