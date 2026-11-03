package upgradectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

type maintenanceBackend interface {
	RegisterCommittedMaintenance(context.Context, protocol.CommittedMaintenancePlan, protocol.RecoveryIdentity) (protocol.CommittedMaintenanceLedger, error)
	LinkCommittedMaintenance(context.Context, protocol.CommittedMaintenanceReceipt) (protocol.CommittedMaintenanceLedger, error)
	CommittedMaintenanceReadiness(context.Context, string) (protocol.CommittedMaintenanceLedger, int64, error)
}

type maintenanceRuntime struct {
	mu       sync.Mutex
	id       string
	process  protocol.RecoveryIdentity
	epoch    string
	sequence uint64
}

// InitializeCommittedMaintenance runs before any HTTP listener or main/runner admission.
// The environment carries a launch hint only: the fixed updater's durable state
// and actual kernel identity must independently agree before registration.
func (c *Controller) InitializeCommittedMaintenance(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	backend, ok := c.app.(maintenanceBackend)
	if !ok {
		return errors.New("upgradectl: committed maintenance ledger backend unavailable")
	}
	process, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return err
	}
	state, err := updater.CallSocket(ctx, c.cfg.UpdaterSocket, "status", updater.ControlRequest{})
	if err != nil {
		return err
	}
	r := state.Maintenance
	if r == nil || r.Plan.MaintenanceID != id || r.Plan.Successor.InstanceID != c.cfg.Instance || r.Plan.Successor.ProcessInstanceID != c.cfg.ProcessInstance || r.Plan.Successor.ArtifactSHA256 != c.cfg.BinarySHA256 || process.LaunchID != c.cfg.ProcessInstance || process.SHA256 != c.cfg.BinarySHA256 || state.Format != 3 {
		return errors.New("upgradectl: committed maintenance startup is not bound to the durable local supervisor")
	}
	if r.Phase != "starting_successor" && r.Phase != "checking_successor" && r.Phase != "successor_committed" && r.Phase != "ledger_linked" {
		return errors.New("upgradectl: updater has not durably authorized successor startup")
	}
	if r.Process.PID != 0 && r.Process != process {
		return errors.New("upgradectl: successor kernel identity changed")
	}
	if err := verifyReceiptRecoveryAuthorization(*r); err != nil {
		return err
	}
	if err := verifyMaintenanceParent(state, r.Plan); err != nil {
		return err
	}
	if _, err := backend.RegisterCommittedMaintenance(ctx, r.Plan, process); err != nil {
		return err
	}
	c.maintenance = &maintenanceRuntime{id: id, process: process, epoch: process.LaunchID}
	return nil
}

func (c *Controller) maintenanceHealth(ctx context.Context, p protocol.CommittedMaintenancePlan) (protocol.CommittedMaintenanceHealth, error) {
	h := protocol.CommittedMaintenanceHealth{MaintenanceID: p.MaintenanceID, Binding: p.Successor, Failures: []string{}}
	rt := c.maintenance
	if rt == nil || rt.id != p.MaintenanceID {
		return h, errors.New("upgradectl: this server did not enter committed maintenance startup")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	backend, ok := c.app.(maintenanceBackend)
	if !ok {
		return h, errors.New("upgradectl: committed maintenance ledger unavailable")
	}
	row, schema, err := backend.CommittedMaintenanceReadiness(ctx, p.MaintenanceID)
	if err != nil {
		return h, err
	}
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return h, err
	}
	if !reflect.DeepEqual(row.Plan, p) || row.Process != rt.process || actual != rt.process {
		return h, errors.New("upgradectl: exact successor process and registration required")
	}
	h.Process = actual
	h.Epoch = rt.epoch
	h.Schema = schema
	h.Protocol = protocol.CurrentRunnerProtocol
	h.Registered = true
	h.Fenced = !row.Linked
	// This is live component/read readiness, separate from initial replacement's
	// MainDrained requirement. Already accepted data is never rolled back.
	if c.cfg.RecoveryReady == nil {
		h.Failures = append(h.Failures, "startup_readiness_unavailable")
	} else if err := c.cfg.RecoveryReady(ctx); err != nil {
		h.Failures = append(h.Failures, "startup_components_not_ready")
	}
	if !h.Fenced {
		h.Failures = append(h.Failures, "admission_fence_missing")
	}
	h.Ready = len(h.Failures) == 0
	if h.Ready {
		rt.sequence++
		h.Sequence = rt.sequence
	}
	return h, nil
}

func (c *Controller) maintenanceTick(ctx context.Context) (bool, error) {
	if c.maintenance == nil {
		return false, nil
	}
	state, err := updater.CallSocket(ctx, c.cfg.UpdaterSocket, "status", updater.ControlRequest{})
	if err != nil {
		return true, err
	}
	r := state.Maintenance
	if r == nil || r.Plan.MaintenanceID != c.maintenance.id || r.Process != c.maintenance.process {
		return true, errors.New("upgradectl: committed maintenance supervisor identity changed")
	}
	if r.Phase == "ledger_linked" {
		return false, nil
	}
	if r.Phase != "successor_committed" || r.Receipt == nil {
		return true, nil
	}
	proof := *r.Receipt
	if proof.Process != c.maintenance.process || proof.Successor.ProcessInstanceID != c.cfg.ProcessInstance || proof.Successor.ArtifactSHA256 != c.cfg.BinarySHA256 || state.Current != r.Target || state.Process != proof.Process || state.CommittedProof == nil || state.CommittedProof.Binding != proof.Successor || state.CommittedProof.Process != proof.Process {
		return true, errors.New("upgradectl: durable maintenance receipt does not prove current successor")
	}
	backend, ok := c.app.(maintenanceBackend)
	if !ok {
		return true, errors.New("upgradectl: committed maintenance ledger backend unavailable")
	}
	if _, err := backend.LinkCommittedMaintenance(ctx, proof); err != nil {
		return true, err
	}
	// If this call fails, DB linkage retries idempotently; stop/start never runs.
	_, err = updater.CallSocket(ctx, c.cfg.UpdaterSocket, "maintenance/link", updater.ControlRequest{MaintenanceReceipt: &proof, ExpectedGeneration: state.Generation})
	return true, err
}

var _ maintenanceBackend = (*app.App)(nil)

func verifyMaintenanceParent(state updater.State, p protocol.CommittedMaintenancePlan) error {
	if p.ParentMaintenanceID == "" {
		return nil
	}
	parent, ok := state.MaintenanceHistory[p.ParentMaintenanceID]
	if !ok || p.ParentPlan == nil || p.ParentMaintenanceID == p.MaintenanceID || parent.Plan.MaintenanceID != p.ParentMaintenanceID || !reflect.DeepEqual(parent.Plan, *p.ParentPlan) || !reflect.DeepEqual(parent.Receipt, p.ParentReceipt) || !parent.Irreversible || (parent.Phase != "checking_successor" && parent.Phase != "successor_committed") || parent.Process != p.PreviousProcess || parent.Plan.Successor != p.Previous || parent.ArchiveAuthorization == nil {
		return errors.New("upgradectl: independent failed maintenance history is not established")
	}
	if !reflect.DeepEqual(p.ReceiptRecoveryStop, parent.Plan.ReceiptRecoveryStop) {
		return errors.New("upgradectl: failed maintenance receipt recovery scope changed")
	}
	if err := verifyReceiptRecoveryAuthorization(parent); err != nil {
		return err
	}
	raw, err := json.Marshal(parent.Plan)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if p.ParentPlanSHA256 != hex.EncodeToString(hash[:]) {
		return errors.New("upgradectl: failed maintenance plan digest changed")
	}
	raw, err = json.Marshal(parent.ArchiveAuthorization)
	if err != nil {
		return err
	}
	hash = sha256.Sum256(raw)
	prepared := parent.ArchiveAuthorization
	if p.ParentArchiveSHA256 != hex.EncodeToString(hash[:]) || !prepared.Preparation.Prepared || prepared.Preparation.Revision != p.PreparationRevision || prepared.ActiveOwnerRequests != 0 {
		return errors.New("upgradectl: failed maintenance admission provenance changed")
	}
	if parent.Plan.ReceiptRecoveryStop == nil && !prepared.HostedDrained {
		return errors.New("upgradectl: failed maintenance hosted drain is not confirmed")
	}
	return nil
}

func verifyReceiptRecoveryAuthorization(m updater.CommittedMaintenance) error {
	if m.Plan.ReceiptRecoveryStop == nil {
		if m.ReceiptRecoveryAuthorized || m.ReceiptRecoveryRequestID != "" {
			return errors.New("upgradectl: ordinary maintenance has receipt recovery authority")
		}
		return nil
	}
	if !m.ReceiptRecoveryAuthorized || !protocol.ValidID(protocol.PrefixRequest, m.ReceiptRecoveryRequestID) || m.ReceiptRecoveryRequestID == m.Plan.RequestID || m.ReceiptRecoveryRequestID == m.Plan.LinkRequestID || m.ArchiveAuthorization == nil || !release.ValidSHA(m.ReceiptRecoveryFirstSHA256) || m.ReceiptRecoveryFirstSHA256 != m.ReceiptRecoverySecondSHA256 {
		return errors.New("upgradectl: explicit receipt recovery authorization is incomplete")
	}
	raw, err := json.Marshal(m.Plan.ReceiptRecoveryStop.Runs)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if m.ReceiptRecoveryFirstSHA256 != hex.EncodeToString(hash[:]) || m.ReceiptRecoveryAuthorization == nil || m.ReceiptRecoveryAuthorization.RequestID != m.ReceiptRecoveryRequestID || !reflect.DeepEqual(m.ReceiptRecoveryAuthorization.FirstCapture, m.Plan.ReceiptRecoveryStop.Runs) || !reflect.DeepEqual(m.ReceiptRecoveryAuthorization.SecondCapture, m.Plan.ReceiptRecoveryStop.Runs) {
		return errors.New("upgradectl: original receipt recovery inventory authorization changed")
	}
	if err := updater.ValidateReceiptRecoveryArchive(m.Plan, *m.ArchiveAuthorization); err != nil {
		return fmt.Errorf("upgradectl: receipt recovery archive authorization: %w", err)
	}
	return nil
}
