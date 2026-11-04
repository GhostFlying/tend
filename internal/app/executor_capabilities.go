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
// transaction. Owners define policy; they cannot manufacture executor evidence.
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
	d := protocol.CapabilityDefinition{CapabilityID: x.Env.Target.ID, Revision: old.Revision + 1, Title: p.Title, Description: p.Description, State: p.State, ContractVersion: p.ContractVersion, VerificationPolicy: p.VerificationPolicy, ToolName: p.ToolName}
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
	if s.AgentSelection == nil {
		return nil
	}
	if err := checkCapabilityReferences(ctx, q, s.AgentSelection.Implementation.RequiredCapabilities); err != nil {
		return err
	}
	return checkCapabilityReferences(ctx, q, s.AgentSelection.Acceptance.RequiredCapabilities)
}

// ReportCapabilityEvidence accepts facts only from the authenticated, current
// runner incarnation. No owner, manager, or worker operation exposes this write.
type ReportCapabilityEvidence struct{}

func (ReportCapabilityEvidence) Operation() string { return protocol.OpRunnerCapabilityEvidence }
func (ReportCapabilityEvidence) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	var p protocol.ReportCapabilityEvidencePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	if x.Env.Target.Type != "host" || x.Env.Target.ID != x.Principal.Subject || p.Evidence.Pin.Identity.ExecutionEnvironmentID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "evidence belongs to the authenticated runner")
	}
	if err := authorizeInventory(ctx, q, x.Principal, p.Runtime); err != nil {
		return err
	}
	if !runtimeSupportsExecutorCapabilities(p.Runtime) {
		return protocol.Errorf(protocol.CodeForbidden, "executor capability evidence requires its durable journal contract")
	}
	current, err := store.GetHostRuntime(ctx, q, x.Principal.Subject)
	if err != nil {
		return err
	}
	if !slices.Contains(p.Runtime.Capabilities, protocol.CapabilityExecutorEvidence) || !slices.Contains(current.Runtime.Capabilities, protocol.CapabilityExecutorEvidence) {
		return protocol.Errorf(protocol.CodeForbidden, "executor evidence feature was not negotiated")
	}
	return nil
}
func (ReportCapabilityEvidence) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ReportCapabilityEvidencePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	e := p.Evidence
	if e.EvidenceID != "" || e.RequestID != "" || e.RecordedAtMillis != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "evidence identity and receipt time are server assigned")
	}
	if err := e.Validate(x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if e.InstanceID != p.Runtime.InstanceID || e.Generation != p.Runtime.Generation {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "evidence incarnation does not match reporting runner")
	}
	inv, err := store.GetAgentInventory(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != inv.Receipt.ServerRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "evidence inventory revision has changed")
	}
	if e.Pin.SnapshotID != inv.Receipt.SnapshotID || e.Pin.PublicSHA256 != inv.Receipt.PublicSHA256 || e.Pin.InventoryRevision != inv.Receipt.LocalRevision {
		retained, err := store.RequiredAgentExecutions(ctx, tx.Q(), x.Principal.Subject)
		if err != nil {
			return Outcome{}, err
		}
		if !slices.Contains(retained, e.Pin) {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "evidence snapshot is neither current nor explicitly retained")
		}
		inv, err = store.GetAgentInventoryAt(ctx, tx.Q(), e.Pin)
		if err != nil {
			return Outcome{}, err
		}
	}
	found := false
	for _, a := range inv.Projection.Agents {
		if a.Identity == e.Pin.Identity && a.Revision == e.Pin.AgentRevision && a.Binding == e.Pin.Runtime && a.RequestedModel == e.Pin.Profile.Model && a.RequestedEffort == e.Pin.Profile.Effort && a.Enabled && slices.Contains(a.Capabilities, e.Capability) {
			found = true
		}
	}
	if !found {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "evidence does not bind a declared executor")
	}
	current, err := store.GetHostRuntime(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return Outcome{}, err
	}
	found = false
	for _, executor := range append(slices.Clone(current.Runtime.AgentExecutors), current.Runtime.AgentContinuations...) {
		if executor.Pin == e.Pin && executor.Available {
			found = true
		}
	}
	if !found {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "evidence executor is not a current adapter observation")
	}
	d, err := store.GetCapabilityDefinition(ctx, tx.Q(), e.Capability.CapabilityID, e.Capability.DefinitionRevision)
	if err != nil {
		return Outcome{}, errNotFound(err, "evidence definition does not exist")
	}
	if e.State == "confirmed" && (e.Source == nil || e.Source.ToolName != d.ToolName) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "evidence tool does not satisfy the definition")
	}
	var duplicateSequence bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM executor_capability_evidence WHERE host_id=? AND agent_id=? AND capability_id=? AND definition_revision=? AND json_extract(evidence_json,'$.instance_id')=? AND json_extract(evidence_json,'$.generation')=? AND json_extract(evidence_json,'$.sequence')=?)`, e.Pin.Identity.ExecutionEnvironmentID, e.Pin.Identity.LocalID, e.Capability.CapabilityID, e.Capability.DefinitionRevision, e.InstanceID, e.Generation, e.Sequence).Scan(&duplicateSequence); err != nil {
		return Outcome{}, err
	}
	if duplicateSequence {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "capability observation sequence already belongs to another request")
	}
	e.EvidenceID = "ce_" + x.Env.RequestID
	e.RequestID, e.RecordedAtMillis = x.Env.RequestID, x.Now.UnixMilli()
	if err := tx.InsertCapabilityEvidence(ctx, e); err != nil {
		return Outcome{}, err
	}
	events, err := recordCapabilityRevalidations(ctx, x, tx, e, current.Runtime)
	if err != nil {
		return Outcome{}, err
	}
	events = append(events, Event{ObjectType: "host", ObjectID: x.Principal.Subject, Kind: "host.capability_evidence_recorded", Body: map[string]any{"evidence_id": e.EvidenceID, "state": e.State, "capability_id": e.Capability.CapabilityID}})
	return Outcome{Result: e, Events: events}, nil
}

func (a *App) CapabilityEvidenceRequest(ctx context.Context, p protocol.Principal, input protocol.AgentInventoryStatusPayload) (protocol.CapabilityEvidenceRequestStatus, error) {
	result := protocol.CapabilityEvidenceRequestStatus{RequestState: "not_recorded"}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := authorizeInventory(ctx, tx.Q(), p, input.Runtime); err != nil {
			return err
		}
		rec, err := store.GetRequest(ctx, tx.Q(), p.DedupDomain(), input.RequestID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if rec.Operation != protocol.OpRunnerCapabilityEvidence {
			return protocol.Errorf(protocol.CodeInvalidRequest, "request is not capability evidence")
		}
		op := operationOf(rec)
		result.Request = &op
		result.RequestState = "recorded"
		result.RequestPayloadHash = rec.PayloadHash
		return nil
	})
	return result, err
}

func currentRunCapabilityMatch(ctx context.Context, q store.Querier, b protocol.RunCapabilityBinding, r protocol.RunnerRuntime, now int64) (protocol.CapabilityMatch, error) {
	all, err := store.AgentCapabilityEvidence(ctx, q, b.Pin.Identity.ExecutionEnvironmentID, b.Pin.Identity.LocalID)
	if err != nil {
		return protocol.CapabilityMatch{}, err
	}
	current := []protocol.ExecutorCapabilityEvidence{}
	for _, e := range all {
		if e.Pin == b.Pin && e.InstanceID == r.InstanceID && e.Generation == r.Generation {
			current = append(current, e)
		}
	}
	return protocol.MatchExecutorCapabilities(b.Pin, b.Required, b.Required, current, now), nil
}
func recordCapabilityRevalidations(ctx context.Context, x *Exec, tx *store.Tx, e protocol.ExecutorCapabilityEvidence, r protocol.RunnerRuntime) ([]Event, error) {
	events := []Event{}
	if e.Kind != "tool_inventory" {
		return events, nil
	}
	runs, err := store.ListOpenRunsOnHost(ctx, tx.Q(), x.Principal.Subject)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		b, err := store.GetRunCapabilityBinding(ctx, tx.Q(), run.RunID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.Pin != e.Pin {
			continue
		}
		match, err := currentRunCapabilityMatch(ctx, tx.Q(), b, r, x.Now.UnixMilli())
		if err != nil {
			return nil, err
		}
		if match.State != "eligible" || !slices.Contains(match.EvidenceIDs, e.EvidenceID) {
			continue
		}
		recovery := protocol.RunCapabilityRevalidation{RequestID: x.Env.RequestID, Pin: b.Pin, RequiredSHA256: b.RequiredSHA256, EvidenceIDs: match.EvidenceIDs, ObservedAtMillis: x.Now.UnixMilli()}
		for _, id := range match.EvidenceIDs {
			fact, err := store.GetCapabilityEvidence(ctx, tx.Q(), id)
			if err != nil {
				return nil, err
			}
			recovery.Evidence = append(recovery.Evidence, fact)
		}
		if err := tx.InsertRunCapabilityRevalidation(ctx, run.RunID, recovery); err != nil {
			return nil, err
		}
		events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.capability_revalidated", Body: map[string]any{"request_id": recovery.RequestID, "evidence_ids": recovery.EvidenceIDs}})
	}
	return events, nil
}
