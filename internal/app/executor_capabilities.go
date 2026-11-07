package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// PutCapability appends a definition revision through the usual request/CAS
// transaction. A definition grants no declaration or executor readiness.
type PutCapability struct{}

func (PutCapability) Operation() string { return "capabilities.put" }
func (PutCapability) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (PutCapability) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.PutCapabilityPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.Env.Target.Type != "capability" || !protocol.AgentInventoryID(x.Env.Target.ID) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid capability identity")
	}
	if err := p.Validate(); err != nil {
		return Outcome{}, err
	}
	old, err := store.GetCapabilityDefinition(ctx, tx.Q(), x.Env.Target.ID, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != old.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "capability revision has changed")
	}
	d := protocol.CapabilityDefinition{CapabilityID: x.Env.Target.ID, Revision: old.Revision + 1, Title: p.Title, Description: p.Description, State: p.State, ContractVersion: p.ContractVersion, VerificationPolicy: p.VerificationPolicy}
	if err := tx.InsertCapabilityDefinition(ctx, d, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: d, Events: []Event{{ObjectType: "capability", ObjectID: d.CapabilityID, ObjectVersion: d.Revision, Kind: "capability.updated", Body: map[string]any{"state": d.State}}}}, nil
}

func checkCapabilityReferences(ctx context.Context, q store.Querier, refs []protocol.CapabilityReference) error {
	if _, err := protocol.CanonicalCapabilityReferences(refs); err != nil {
		return err
	}
	for _, ref := range refs {
		d, err := store.GetCapabilityDefinition(ctx, q, ref.CapabilityID, ref.DefinitionRevision)
		if err != nil {
			return errNotFound(err, "required capability revision does not exist")
		}
		current, err := store.GetCapabilityDefinition(ctx, q, ref.CapabilityID, 0)
		if err != nil {
			return err
		}
		if d.State != "active" || current.State != "active" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "retired capability cannot be used for a new declaration or task")
		}
	}
	return nil
}

func checkSpecCapabilities(ctx context.Context, q store.Querier, s domain.Spec) error {
	return checkSpecCapabilityUpdates(ctx, q, domain.Spec{}, s)
}

// References retained by the same role keep their frozen historical meaning.
// Only a new exact reference needs an active definition, regardless of mounts.
func checkSpecCapabilityUpdates(ctx context.Context, q store.Querier, current, next domain.Spec) error {
	if next.AgentSelection == nil {
		return nil
	}
	var previous protocol.TaskAgentSelection
	if current.AgentSelection != nil {
		previous = *current.AgentSelection
	}
	if err := checkAddedCapabilityReferences(ctx, q, previous.Implementation.RequiredCapabilities, next.AgentSelection.Implementation.RequiredCapabilities); err != nil {
		return err
	}
	return checkAddedCapabilityReferences(ctx, q, previous.Acceptance.RequiredCapabilities, next.AgentSelection.Acceptance.RequiredCapabilities)
}

func checkAddedCapabilityReferences(ctx context.Context, q store.Querier, before, after []protocol.CapabilityReference) error {
	refs, err := protocol.CanonicalCapabilityReferences(after)
	if err != nil {
		return err
	}
	var added []protocol.CapabilityReference
	for _, ref := range refs {
		if !slices.Contains(before, ref) {
			added = append(added, ref)
		}
	}
	return checkCapabilityReferences(ctx, q, added)
}

func currentRunCapabilityMatch(ctx context.Context, q store.Querier, b protocol.RunCapabilityBinding, r protocol.RunnerRuntime, now int64) (protocol.CapabilityMatch, error) {
	if b.MatchPolicy != "declaration_v1" {
		return protocol.CapabilityMatch{}, protocol.Errorf(protocol.CodeInvalidTransition, "unsupported capability match policy")
	}
	// Read declarations from the frozen inventory, never the latest Agent.
	inv, err := store.GetAgentInventoryAt(ctx, q, b.Pin)
	if err != nil {
		return protocol.CapabilityMatch{}, err
	}
	var declared []protocol.CapabilityReference
	for _, agent := range inv.Projection.Agents {
		if agent.Identity == b.Pin.Identity && agent.Revision == b.Pin.AgentRevision && agent.Binding == b.Pin.Runtime && agent.RequestedModel == b.Pin.Profile.Model && agent.RequestedEffort == b.Pin.Profile.Effort {
			declared = agent.Capabilities
		}
	}
	m := protocol.MatchExecutorCapabilities(b.Pin, b.Required, declared, now)
	host, err := store.GetHostRuntime(ctx, q, b.Pin.Identity.ExecutionEnvironmentID)
	if err != nil {
		return m, err
	}
	available := false
	for _, e := range append(slices.Clone(r.AgentExecutors), r.AgentContinuations...) {
		observed, original := e.Pin, b.Pin
		observed.SnapshotID, original.SnapshotID = "", ""
		observed.InventoryRevision, original.InventoryRevision = 0, 0
		observed.PublicSHA256, original.PublicSHA256 = "", ""
		if e.Available && observed == original {
			available = true
		}
	}
	if !host.Online || now-host.LastHeartbeatAt >= 60000 || !available {
		if m.State == "eligible" {
			m.State = "pending"
		}
		for i := range m.Reasons {
			if m.Reasons[i].State == "eligible" {
				m.Reasons[i].State, m.Reasons[i].Reason, m.Reasons[i].Recovery = "pending", "original_executor_unavailable", "restore the same frozen executor; do not switch identity or provider"
			}
		}
	}
	return m, nil
}
