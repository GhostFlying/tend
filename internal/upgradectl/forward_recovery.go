package upgradectl

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

type recoveryBackend interface {
	RegisterForwardRecovery(context.Context, protocol.ForwardRecoveryPlan, protocol.RecoveryIdentity) (protocol.ForwardRecoveryLedger, error)
	LinkForwardRecovery(context.Context, protocol.RecoveryReceipt) (protocol.ForwardRecoveryLedger, error)
	ForwardRecoveryReadiness(context.Context, string) (protocol.ForwardRecoveryLedger, int64, error)
}

type recoveryRuntime struct {
	mu       sync.Mutex
	id       string
	process  protocol.RecoveryIdentity
	epoch    string
	sequence uint64
}

// InitializeRecovery runs before any HTTP listener or main/runner admission.
// The environment carries a launch hint only: the fixed updater's durable state
// and actual kernel identity must independently agree before registration.
func (c *Controller) InitializeRecovery(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	backend, ok := c.app.(recoveryBackend)
	if !ok {
		return errors.New("upgradectl: recovery ledger backend unavailable")
	}
	process, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return err
	}
	state, err := updater.CallSocket(ctx, c.cfg.UpdaterSocket, "status", updater.ControlRequest{})
	if err != nil {
		return err
	}
	r := state.Recovery
	if r == nil || r.Plan.RecoveryID != id || r.Plan.Successor.InstanceID != c.cfg.Instance || r.Plan.Successor.ProcessInstanceID != c.cfg.ProcessInstance || r.Plan.Successor.ArtifactSHA256 != c.cfg.BinarySHA256 || process.LaunchID != c.cfg.ProcessInstance || process.SHA256 != c.cfg.BinarySHA256 || state.Format != 2 || state.UpdaterEpoch == "" {
		return errors.New("upgradectl: recovery startup is not bound to the durable local supervisor")
	}
	if r.Phase != "starting_successor" && r.Phase != "checking_successor" && r.Phase != "successor_committed" && r.Phase != "ledger_linked" {
		return errors.New("upgradectl: updater has not durably authorized successor startup")
	}
	if r.Process.PID != 0 && r.Process != process {
		return errors.New("upgradectl: successor kernel identity changed")
	}
	if _, err := backend.RegisterForwardRecovery(ctx, r.Plan, process); err != nil {
		return err
	}
	c.recovery = &recoveryRuntime{id: id, process: process, epoch: process.LaunchID}
	return nil
}

func (c *Controller) recoveryHealth(ctx context.Context, p protocol.ForwardRecoveryPlan) (protocol.RecoveryHealth, error) {
	h := protocol.RecoveryHealth{RecoveryID: p.RecoveryID, Binding: p.Successor, Failures: []string{}}
	rt := c.recovery
	if rt == nil || rt.id != p.RecoveryID {
		return h, errors.New("upgradectl: this server did not enter recovery startup")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	backend, ok := c.app.(recoveryBackend)
	if !ok {
		return h, errors.New("upgradectl: recovery ledger unavailable")
	}
	row, schema, err := backend.ForwardRecoveryReadiness(ctx, p.RecoveryID)
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

func (c *Controller) recoveryTick(ctx context.Context) (bool, error) {
	if c.recovery == nil {
		return false, nil
	}
	state, err := updater.CallSocket(ctx, c.cfg.UpdaterSocket, "status", updater.ControlRequest{})
	if err != nil {
		return true, err
	}
	r := state.Recovery
	if r == nil || r.Plan.RecoveryID != c.recovery.id || r.Process != c.recovery.process {
		return true, errors.New("upgradectl: recovery supervisor identity changed")
	}
	if r.Phase == "ledger_linked" {
		return false, nil
	}
	if r.Phase != "successor_committed" || r.Receipt == nil {
		return true, nil
	}
	proof := *r.Receipt
	if proof.Process != c.recovery.process || proof.Successor.ProcessInstanceID != c.cfg.ProcessInstance || proof.Successor.ArtifactSHA256 != c.cfg.BinarySHA256 || state.Current != r.Target || state.Process != proof.Process || state.CommittedProof == nil || state.CommittedProof.Binding != proof.Successor || state.CommittedProof.Process != proof.Process {
		return true, errors.New("upgradectl: durable forward receipt does not prove current successor")
	}
	backend, ok := c.app.(recoveryBackend)
	if !ok {
		return true, errors.New("upgradectl: recovery ledger backend unavailable")
	}
	if _, err := backend.LinkForwardRecovery(ctx, proof); err != nil {
		return true, err
	}
	// If this call fails, DB linkage retries idempotently; stop/start never runs.
	_, err = updater.CallSocket(ctx, c.cfg.UpdaterSocket, "recovery/link", updater.ControlRequest{Receipt: &proof, ExpectedGeneration: state.Generation})
	return true, err
}

var _ recoveryBackend = (*app.App)(nil)
