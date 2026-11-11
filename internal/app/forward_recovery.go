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

const ForwardRecoveryDestination = "local-forward-recovery"

// These operations are invoked by local owner handlers under the system
// principal. They remain owner mutations and cannot bypass startup admission.
func recoveryOwnerControl(operation string) bool {
	switch operation {
	case "upgrades.start", "upgrades.control", "archive.control", "hosts.maintenance":
		return true
	}
	return false
}

// WithForwardRecoveryStartup fences all owner/main writes and runner claims
// from App construction, including before the recovery registration transaction.
func WithForwardRecoveryStartup(id string) Option { return func(a *App) { a.forwardRecoveryID = id } }
func (a *App) recoveryStartupFenced(ctx context.Context, q store.Querier) (bool, error) {
	if fenced, err := a.committedMaintenanceFenced(ctx, q); err != nil || fenced {
		return fenced, err
	}
	if a.forwardRecoveryID == "" {
		return store.ForwardRecoveryFenced(ctx, q)
	}
	r, err := store.GetForwardRecovery(ctx, q, a.forwardRecoveryID)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	return !r.Linked, err
}

// recoveryReceiptWrite admits only terminal native history for an existing
// thread/turn. Reconciled is set by the native-history adapter, never a payload.
func recoveryReceiptWrite(ctx context.Context, x *Exec, q store.Querier, cmd Command) (bool, error) {
	if _, claim := cmd.(Claim); claim && x.Principal.Kind == protocol.PrincipalRunner {
		var payload protocol.ClaimPayload
		if err := decodePayload(x.Env, &payload); err != nil {
			return false, err
		}
		if payload.RunID == "" {
			return false, nil
		}
		actual, err := store.GetRun(ctx, q, payload.RunID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return actual.HostID == x.Principal.Subject, nil
	}

	record, ok := cmd.(RecordAgentMessage)
	if !ok || !record.Reconciled || x.Principal.Kind != protocol.PrincipalMainAgent {
		return false, nil
	}
	var p recordAgentMessagePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return false, err
	}
	if x.Principal.Thread == "" || p.TurnID == "" || p.ItemID == "" {
		return false, nil
	}
	switch p.Status {
	case protocol.AssistantCompleted, protocol.AssistantInterrupted, protocol.AssistantFailed:
	default:
		return false, nil
	}
	_, err := store.GetMainTurnByTurn(ctx, q, x.Principal.Thread, p.TurnID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

type registerForwardRecovery struct {
	Plan    protocol.ForwardRecoveryPlan
	Process protocol.RecoveryIdentity
}

func (registerForwardRecovery) Operation() string { return "recovery.register" }
func (registerForwardRecovery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c registerForwardRecovery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	p := c.Plan
	if !release.ValidID(p.RecoveryID) || !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || c.Process.PID <= 1 || c.Process.StartTicks == 0 || c.Process.LaunchID != p.Successor.ProcessInstanceID || c.Process.SHA256 != p.Successor.ArtifactSHA256 || p.Successor.InstanceID != p.Original.InstanceID || p.Successor.UpgradeID != p.RecoveryID || p.Successor == p.Original {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid independent recovery binding")
	}
	old, err := store.GetForwardRecovery(ctx, tx.Q(), p.RecoveryID)
	if err == nil {
		if !reflect.DeepEqual(old.Plan, p) || old.Process != c.Process {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "recovery registration changed")
		}
		return Outcome{Result: old}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	r, err := store.GetUpgrade(ctx, tx.Q(), p.Original.UpgradeID)
	if err != nil {
		return Outcome{}, err
	}
	if !reflect.DeepEqual(r.View, p.OriginalLedger) || p.LedgerRevision != r.View.Revision || !bindingMatches(r, p.Original) || r.ServerProcess != p.Original.ProcessInstanceID || r.ServerInstance != p.Original.InstanceID || r.ServerHash != p.Original.ArtifactSHA256 || r.View.Status != "completed" {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original terminal ledger binding/view changed")
	}
	if r.StartRequestID != p.OriginalRequestID {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "original start does not identify this upgrade")
	}
	snapshot, err := json.Marshal(r)
	if err != nil {
		return Outcome{}, err
	}
	snapshotHash := sha256.Sum256(snapshot)
	value := protocol.ForwardRecoveryLedger{Plan: p, Process: c.Process, OriginalSnapshot: snapshot, OriginalSnapshotSHA256: hex.EncodeToString(snapshotHash[:])}
	if err := tx.PutForwardRecovery(ctx, value, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: value, Events: []Event{{ObjectType: "recovery", ObjectID: p.RecoveryID, ObjectVersion: 1, Kind: "forward_recovery_registered", Body: map[string]any{"original_upgrade_id": p.Original.UpgradeID, "original_proof_status": "unconfirmed", "admission_fenced": true}}}}, nil
}

type linkForwardRecovery struct{ Receipt protocol.RecoveryReceipt }

func (linkForwardRecovery) Operation() string { return "recovery.link" }
func (linkForwardRecovery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c linkForwardRecovery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	proof := c.Receipt
	r, err := store.GetForwardRecovery(ctx, tx.Q(), proof.RecoveryID)
	if err != nil {
		return Outcome{}, err
	}
	if proof.Original != r.Plan.Original || proof.Successor != r.Plan.Successor || proof.RequestID != r.Plan.RequestID || proof.Process != r.Process || proof.Generation == 0 || proof.Epoch == "" || proof.FirstSequence == 0 || proof.SecondSequence <= proof.FirstSequence {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "exact independent forward commit receipt required")
	}
	if r.Linked {
		if r.Receipt == nil || *r.Receipt != proof {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "linked receipt changed")
		}
		return Outcome{Result: r}, nil
	}
	r.Receipt = &proof
	r.Linked = true
	if err := tx.PutForwardRecovery(ctx, r, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	// Durable outbox is a notification only, never another control action.
	return Outcome{Result: r, Events: []Event{{ObjectType: "recovery", ObjectID: proof.RecoveryID, ObjectVersion: 2, Kind: "forward_recovery_linked", Body: map[string]any{"original_upgrade_id": proof.Original.UpgradeID, "original_proof_status": "unconfirmed", "successor_committed": true, "admission_fenced": false}}}, Outbox: []OutboxMessage{{Destination: ForwardRecoveryDestination, Operation: "recovery.linked", Payload: map[string]any{"recovery_id": proof.RecoveryID, "original_upgrade_id": proof.Original.UpgradeID}}}}, nil
}
func (a *App) RegisterForwardRecovery(ctx context.Context, p protocol.ForwardRecoveryPlan, process protocol.RecoveryIdentity) (protocol.ForwardRecoveryLedger, error) {
	raw, _ := json.Marshal(struct {
		Plan    protocol.ForwardRecoveryPlan `json:"plan"`
		Process protocol.RecoveryIdentity    `json:"process"`
	}{p, process})
	env := &protocol.Envelope{RequestID: p.RequestID, Operation: "recovery.register", Target: protocol.Target{Type: "recovery", ID: p.RecoveryID}, Payload: raw, Reason: "Register exact locally authorized forward recovery before admission."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, registerForwardRecovery{Plan: p, Process: process})
	if err != nil {
		return protocol.ForwardRecoveryLedger{}, err
	}
	if op.Error != nil {
		return protocol.ForwardRecoveryLedger{}, op.Error
	}
	var r protocol.ForwardRecoveryLedger
	err = json.Unmarshal(op.Result, &r)
	return r, err
}
func (a *App) LinkForwardRecovery(ctx context.Context, proof protocol.RecoveryReceipt) (protocol.ForwardRecoveryLedger, error) {
	recovery, _, err := a.ForwardRecoveryReadiness(ctx, proof.RecoveryID)
	if err != nil {
		return protocol.ForwardRecoveryLedger{}, err
	}
	raw, _ := json.Marshal(proof)
	// The immutable receipt identity makes crashes between file and DB commits
	// replayable. This separate request never reuses the original start request.
	env := &protocol.Envelope{RequestID: recovery.Plan.LinkRequestID, Operation: "recovery.link", ParentRequestID: proof.RequestID, Target: protocol.Target{Type: "recovery", ID: proof.RecoveryID}, Payload: raw, Reason: "Append durable forward commit receipt and release its admission fence."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, linkForwardRecovery{Receipt: proof})
	if err != nil {
		return protocol.ForwardRecoveryLedger{}, err
	}
	if op.Error != nil {
		return protocol.ForwardRecoveryLedger{}, op.Error
	}
	var r protocol.ForwardRecoveryLedger
	err = json.Unmarshal(op.Result, &r)
	return r, err
}
func (a *App) ForwardRecoveryReadiness(ctx context.Context, id string) (protocol.ForwardRecoveryLedger, int64, error) {
	var r protocol.ForwardRecoveryLedger
	var schema int64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		r, err = store.GetForwardRecovery(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
	})
	return r, schema, err
}
