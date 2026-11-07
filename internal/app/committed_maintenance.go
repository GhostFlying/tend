package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// WithCommittedMaintenanceStartup fences admission before registration. The
// controller separately validates the hint against the fixed local supervisor.
func WithCommittedMaintenanceStartup(id string) Option {
	return func(a *App) { a.committedMaintenanceID = id }
}

func (a *App) committedMaintenanceFenced(ctx context.Context, q store.Querier) (bool, error) {
	if a.committedMaintenanceID != "" {
		row, err := store.GetCommittedMaintenance(ctx, q, a.committedMaintenanceID)
		if errors.Is(err, store.ErrNotFound) {
			return true, nil
		}
		if err != nil || !row.Linked {
			return !row.Linked, err
		}
	}
	return store.CommittedMaintenanceFenced(ctx, q)
}

type registerCommittedMaintenance struct {
	Bounded *protocol.BoundedMaintenanceState
	Plan    protocol.CommittedMaintenancePlan
	Process protocol.RecoveryIdentity
}

func (registerCommittedMaintenance) Operation() string { return "maintenance.register" }
func (registerCommittedMaintenance) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c registerCommittedMaintenance) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	p, process := c.Plan, c.Process
	independentArtifact := p.Successor.ArtifactSHA256 != p.Previous.ArtifactSHA256 || p.SourceOperation != nil && p.SourceOperation.Kind == "restart"
	if !release.ValidID(p.MaintenanceID) || !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || !protocol.ValidID(protocol.PrefixRequest, p.LinkRequestID) || p.RequestID == p.LinkRequestID || p.PreparationRevision < 1 || process.PID <= 1 || process.StartTicks == 0 || process.LaunchID != p.Successor.ProcessInstanceID || !release.ValidSHA(process.SHA256) || process.SHA256 != p.Successor.ArtifactSHA256 || p.Successor.InstanceID == "" || p.Successor.InstanceID != p.Previous.InstanceID || p.Successor.UpgradeID != p.MaintenanceID || p.Successor.ProcessInstanceID == p.Previous.ProcessInstanceID || !independentArtifact {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid independent committed maintenance binding")
	}
	old, err := store.GetCommittedMaintenance(ctx, tx.Q(), p.MaintenanceID)
	if err == nil {
		if !reflect.DeepEqual(old.Plan, p) || old.Process != process {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "committed maintenance registration changed")
		}
		return Outcome{Result: old}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	prepared, err := store.ArchivePreparation(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if c.Bounded == nil && (!prepared.Prepared || prepared.Revision != p.PreparationRevision) {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "committed maintenance preparation changed")
	}
	if err := validateCommittedMaintenanceParent(ctx, tx.Q(), p); err != nil {
		return Outcome{}, err
	}
	fenced, err := store.CommittedMaintenanceFencedExcept(ctx, tx.Q(), p.ParentMaintenanceID)
	if err != nil {
		return Outcome{}, err
	}
	if fenced {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "another committed maintenance is awaiting linkage")
	}
	successor, err := store.CommittedMaintenanceHasSuccessor(ctx, tx.Q(), p.ParentMaintenanceID)
	if err != nil {
		return Outcome{}, err
	}
	if p.ParentMaintenanceID != "" && successor {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "maintenance parent already has an independent successor")
	}
	row := protocol.CommittedMaintenanceLedger{Plan: p, Process: process}
	if err := tx.PutCommittedMaintenance(ctx, row, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	var captureErr error
	if c.Bounded != nil {
		captureErr = captureBoundedMaintenanceRunProtection(ctx, x, tx, p)
	} else {
		captureErr = captureMaintenanceRunProtection(ctx, x, tx, p)
	}
	if captureErr != nil {
		return Outcome{}, captureErr
	}
	events := []Event{{ObjectType: "maintenance", ObjectID: p.MaintenanceID, ObjectVersion: 1, Kind: "committed_maintenance_registered", Body: map[string]any{"admission_fenced": true}}}
	if p.SourceOperation != nil {
		original, err := maintenanceOperationSource(ctx, tx.Q(), p)
		if err != nil {
			return Outcome{}, err
		}
		events = append(events, Event{ObjectType: "maintenance", ObjectID: p.MaintenanceID, Kind: "maintenance.source_operation_retained", Body: map[string]any{"source_operation": p.SourceOperation, "original": original, "original_health_unconfirmed": true}})
	}
	if c.Bounded != nil {
		sources, err := boundedSummarySources(ctx, tx.Q())
		if err != nil {
			return Outcome{}, err
		}
		events = append(events, Event{ObjectType: "maintenance", ObjectID: p.MaintenanceID, Kind: "maintenance.bounded_force_registered", Body: map[string]any{"bounded": c.Bounded, "summary_unknowns": sources}})
	}
	return Outcome{Result: row, Events: events}, nil
}

type linkCommittedMaintenance struct {
	Receipt protocol.CommittedMaintenanceReceipt
}

func (linkCommittedMaintenance) Operation() string { return "maintenance.link" }
func (linkCommittedMaintenance) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c linkCommittedMaintenance) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	proof := c.Receipt
	row, err := store.GetCommittedMaintenance(ctx, tx.Q(), proof.MaintenanceID)
	if err != nil {
		return Outcome{}, err
	}
	if proof.Previous != row.Plan.Previous || proof.Successor != row.Plan.Successor || proof.RequestID != row.Plan.RequestID || proof.Process != row.Process || proof.Generation == 0 || proof.Epoch != row.Process.LaunchID || proof.FirstSequence == 0 || proof.SecondSequence <= proof.FirstSequence {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "exact independent committed maintenance receipt required")
	}
	if row.Linked {
		if row.Receipt == nil || *row.Receipt != proof {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "linked maintenance receipt changed")
		}
		return Outcome{Result: row}, nil
	}
	successor, err := store.CommittedMaintenanceHasSuccessor(ctx, tx.Q(), proof.MaintenanceID)
	if err != nil {
		return Outcome{}, err
	}
	if successor {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "maintenance responsibility passed to an independent successor")
	}
	prepared, err := store.ArchivePreparation(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	var forced bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE object_type='maintenance' AND object_id=? AND kind='maintenance.bounded_force_registered')`, proof.MaintenanceID).Scan(&forced); err != nil {
		return Outcome{}, err
	}
	if !forced && (!prepared.Prepared || prepared.Revision != row.Plan.PreparationRevision) {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "committed maintenance preparation changed before linkage")
	}
	if err := supersedeMaintenanceOperation(ctx, x, tx, row.Plan); err != nil {
		return Outcome{}, err
	}
	row.Linked = true
	row.Receipt = &proof
	if err := tx.PutCommittedMaintenance(ctx, row, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	// Archive and host controls remain owned by the explicit finish operation.
	return Outcome{Result: row, Events: []Event{{ObjectType: "maintenance", ObjectID: proof.MaintenanceID, ObjectVersion: 2, Kind: "committed_maintenance_linked", Body: map[string]any{"successor_committed": true, "admission_fenced": false}}}}, nil
}

func (a *App) RegisterCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, process protocol.RecoveryIdentity) (protocol.CommittedMaintenanceLedger, error) {
	return a.registerCommittedMaintenance(ctx, p, process, nil)
}

func (a *App) RegisterBoundedCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, process protocol.RecoveryIdentity, b protocol.BoundedMaintenanceState) (protocol.CommittedMaintenanceLedger, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return protocol.CommittedMaintenanceLedger{}, err
	}
	sum := sha256.Sum256(raw)
	if b.Authorization.Role != "server" || b.Authorization.PlanSHA256 != hex.EncodeToString(sum[:]) || b.Authorization.Source != p.PreviousProcess || b.Authorization.Target != p.Successor || b.SourceExit == nil || b.SourceExit.Process != p.PreviousProcess || b.SourceExit.ConfirmedAtMillis <= 0 || !protocol.ValidID(protocol.PrefixRequest, b.Authorization.RequestID) || b.Authorization.Reason == "" {
		return protocol.CommittedMaintenanceLedger{}, protocol.Errorf(protocol.CodeInvalidRequest, "independent bounded server startup authority required")
	}
	frozen := protocol.BoundedMaintenanceState{Authorization: b.Authorization, SourceExit: b.SourceExit}
	for _, entry := range b.Audit {
		if entry.RequestID == b.Authorization.RequestID {
			frozen.Audit = []protocol.BoundedMaintenanceAudit{entry}
			break
		}
	}
	return a.registerCommittedMaintenance(ctx, p, process, &frozen)
}

func (a *App) registerCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, process protocol.RecoveryIdentity, bounded *protocol.BoundedMaintenanceState) (protocol.CommittedMaintenanceLedger, error) {
	raw, err := json.Marshal(struct {
		Plan    protocol.CommittedMaintenancePlan `json:"plan"`
		Process protocol.RecoveryIdentity         `json:"process"`
		Bounded *protocol.BoundedMaintenanceState `json:"bounded,omitempty"`
	}{p, process, bounded})
	if err != nil {
		return protocol.CommittedMaintenanceLedger{}, err
	}
	requestID := p.RequestID
	parentRequestID := ""
	if bounded != nil {
		requestID = bounded.Authorization.RequestID
		parentRequestID = p.RequestID
	}
	env := &protocol.Envelope{RequestID: requestID, ParentRequestID: parentRequestID, Operation: "maintenance.register", Target: protocol.Target{Type: "maintenance", ID: p.MaintenanceID}, Payload: raw, Reason: "Register the exact locally authorized committed maintenance process before admission."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, registerCommittedMaintenance{Plan: p, Process: process, Bounded: bounded})
	if err != nil {
		return protocol.CommittedMaintenanceLedger{}, err
	}
	if op.Error != nil {
		return protocol.CommittedMaintenanceLedger{}, op.Error
	}
	var row protocol.CommittedMaintenanceLedger
	err = json.Unmarshal(op.Result, &row)
	return row, err
}
func (a *App) LinkCommittedMaintenance(ctx context.Context, proof protocol.CommittedMaintenanceReceipt) (protocol.CommittedMaintenanceLedger, error) {
	row, _, err := a.CommittedMaintenanceReadiness(ctx, proof.MaintenanceID)
	if err != nil {
		return row, err
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return row, err
	}
	env := &protocol.Envelope{RequestID: row.Plan.LinkRequestID, Operation: "maintenance.link", ParentRequestID: proof.RequestID, Target: protocol.Target{Type: "maintenance", ID: proof.MaintenanceID}, Payload: raw, Reason: "Link the exact durable maintenance commit receipt without changing retained business runs."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, linkCommittedMaintenance{Receipt: proof})
	if err != nil {
		return protocol.CommittedMaintenanceLedger{}, err
	}
	if op.Error != nil {
		return protocol.CommittedMaintenanceLedger{}, op.Error
	}
	err = json.Unmarshal(op.Result, &row)
	return row, err
}
func (a *App) CommittedMaintenanceReadiness(ctx context.Context, id string) (protocol.CommittedMaintenanceLedger, int64, error) {
	var row protocol.CommittedMaintenanceLedger
	var schema int64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		row, err = store.GetCommittedMaintenance(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
	})
	return row, schema, err
}

func validateCommittedMaintenanceParent(ctx context.Context, q store.Querier, p protocol.CommittedMaintenancePlan) error {
	if p.SourceOperation != nil {
		_, err := maintenanceOperationSource(ctx, q, p)
		return err
	}
	if p.ParentMaintenanceID == "" {
		if p.ParentPlan != nil || p.ParentReceipt != nil || p.ParentLaunchExit != nil || p.ParentPlanSHA256 != "" || p.ParentArchiveSHA256 != "" || p.ParentGeneration != 0 || p.FailedProcessExited {
			return protocol.Errorf(protocol.CodeInvalidRequest, "maintenance parent provenance is incomplete")
		}
		return nil
	}
	parent := p.ParentPlan
	if parent != nil && (p.RequestID == parent.RequestID || p.RequestID == parent.LinkRequestID || p.LinkRequestID == parent.RequestID || p.LinkRequestID == parent.LinkRequestID) {
		return protocol.Errorf(protocol.CodeConflictPayload, "failed maintenance successor requires independent request identities")
	}
	if parent == nil || parent.MaintenanceID != p.ParentMaintenanceID || p.ParentMaintenanceID == p.MaintenanceID || p.ParentGeneration == 0 || p.ParentGeneration != p.ExpectedGeneration || !release.ValidSHA(p.ParentArchiveSHA256) || parent.Successor != p.Previous || parent.PreparationRevision != p.PreparationRevision || parent.PreparationRequestID != p.PreparationRequestID || parent.PreparationCancelRequestID != p.PreparationCancelRequestID || !reflect.DeepEqual(parent.HostControls, p.HostControls) || !reflect.DeepEqual(parent.ReceiptRecoveryStop, p.ReceiptRecoveryStop) {
		return protocol.Errorf(protocol.CodeConflictPayload, "independent failed maintenance provenance changed")
	}
	if p.PreviousProcess == (protocol.RecoveryIdentity{}) && p.ParentLaunchExit == nil {
		return protocol.Errorf(protocol.CodeConflictPayload, "failed maintenance requires original process or owned launch exit provenance")
	}
	if proof := p.ParentLaunchExit; proof != nil {
		if !p.FailedProcessExited || p.PreviousProcess != (protocol.RecoveryIdentity{}) || p.ParentReceipt != nil || proof.PID <= 1 || proof.LaunchID != parent.Successor.ProcessInstanceID || proof.ArtifactSHA256 != parent.Successor.ArtifactSHA256 || !release.ValidSHA(proof.ArtifactSHA256) || proof.ExitCode < -1 || proof.ExitCode > 255 || proof.Signal < 0 || proof.Signal > 64 || (proof.Signal == 0 && proof.ExitCode < 0) || (proof.Signal != 0 && proof.ExitCode != -1) {
			return protocol.Errorf(protocol.CodeConflictPayload, "failed maintenance launch exit provenance changed")
		}
	}
	raw, err := json.Marshal(parent)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if p.ParentPlanSHA256 != hex.EncodeToString(hash[:]) {
		return protocol.Errorf(protocol.CodeConflictPayload, "failed maintenance parent digest changed")
	}
	stored, err := store.GetCommittedMaintenance(ctx, q, p.ParentMaintenanceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	processMatches := stored.Process == p.PreviousProcess
	if proof := p.ParentLaunchExit; proof != nil {
		// The candidate may have registered its own real kernel identity before
		// the updater's first Inspect. Preserve that identity, including start
		// ticks unavailable to the updater's independently owned Wait proof.
		processMatches = stored.Process.StartTicks != 0 && stored.Process.PID == proof.PID && stored.Process.LaunchID == proof.LaunchID && stored.Process.SHA256 == proof.ArtifactSHA256
	}
	if !reflect.DeepEqual(stored.Plan, *parent) || !processMatches {
		return protocol.Errorf(protocol.CodeConflictPayload, "failed maintenance parent ledger changed")
	}
	// The updater acknowledgment can be lost after a successful DB link. Its
	// exact receipt remains a fact; a child must not rewrite it as unlinked.
	if stored.Linked && (stored.Receipt == nil || p.ParentReceipt == nil || *stored.Receipt != *p.ParentReceipt) {
		return protocol.Errorf(protocol.CodeConflictPayload, "failed maintenance parent ledger changed")
	}
	return nil
}
