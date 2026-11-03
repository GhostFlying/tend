package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// ForwardRecovery retains the original full state independently of its successor.
// It is never an Operation and never produces an original terminal receipt.
type ForwardRecovery struct {
	AuthorizationBootID         string `json:"authorization_boot_id,omitempty"`
	AuthorizationDeadlineMillis int64  `json:"authorization_deadline_millis,omitempty"`

	Plan                protocol.ForwardRecoveryPlan `json:"plan"`
	OriginalState       json.RawMessage              `json:"original_state"`
	OriginalProof       *CommittedProof              `json:"original_proof,omitempty"`
	Target              Version                      `json:"target"`
	Phase               string                       `json:"phase"`
	Process             Identity                     `json:"process"`
	Epoch               string                       `json:"epoch,omitempty"`
	FirstSequence       uint64                       `json:"first_sequence,omitempty"`
	LastSequence        uint64                       `json:"last_sequence,omitempty"`
	HealthCount         int                          `json:"health_count"`
	Receipt             *protocol.RecoveryReceipt    `json:"receipt,omitempty"`
	LinkedReceiptSHA256 string                       `json:"linked_receipt_sha256,omitempty"`
	LastError           string                       `json:"last_error,omitempty"`
}

type RecoveryAuthority interface {
	RecoveryHealth(context.Context, protocol.ForwardRecoveryPlan) (protocol.RecoveryHealth, error)
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	return bytesDigest(raw)
}
func bytesDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func validIdentity(id Identity) bool {
	return id.PID > 1 && id.StartTicks > 0 && id.LaunchID != "" && release.ValidSHA(id.SHA256)
}

func (s *Supervisor) validateRecovery(ctx context.Context, p protocol.ForwardRecoveryPlan) error {
	if s.cfg.Role != "server" || !release.ValidID(p.RecoveryID) || !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || !protocol.ValidID(protocol.PrefixRequest, p.LinkRequestID) || p.LinkRequestID == p.RequestID || p.LinkRequestID == p.OriginalRequestID || p.RequestID == p.OriginalRequestID || !protocol.ValidID(protocol.PrefixRequest, p.OriginalRequestID) || p.OperatorUID != os.Getuid() || p.WindowClaim == "" || p.LedgerRevision < 1 || p.OriginalLedger.UpgradeID != p.Original.UpgradeID || p.OriginalLedger.Revision != p.LedgerRevision || p.OriginalLedger.Status != "completed" || p.ExpiresAtMillis <= s.opts.Now().UnixMilli() || p.ExpiresAtMillis > s.opts.Now().Add(30*time.Minute).UnixMilli() {
		return errors.New("updater: incomplete or expired forward maintenance authorization")
	}
	if s.cfg.AuthoritySocket == "" || s.cfg.CoordinatorSocket == "" {
		return errors.New("updater: recovery requires fixed private server authority and coordinator sockets")
	}
	if p.ExpectedGeneration != s.state.Generation || p.StateSHA256 != digest(s.state) || p.ConfigSHA256 != digest(s.cfg) {
		return errors.New("updater: recovery state/configuration generation changed")
	}
	if err := verifyResourceEvidence(p, s.opts.Now()); err != nil {
		return err
	}
	op := s.state.Operation
	if op == nil || op.Kind != "upgrade" || op.Phase != "checking" || !op.Irreversible || op.Binding != p.Original || op.CandidateProcess != p.OriginalProcess || s.state.Process != p.OriginalProcess || p.Original.ProcessInstanceID != p.OriginalProcess.LaunchID || p.Original.ArtifactSHA256 != p.OriginalProcess.SHA256 {
		return errors.New("updater: recovery requires the exact irreversible checking operation")
	}
	if p.Successor.UpgradeID != p.RecoveryID || p.Successor.InstanceID != s.cfg.InstanceID || !validRollbackBinding(p.Successor, s.cfg.InstanceID) || p.Successor.ProcessInstanceID == p.Original.ProcessInstanceID || p.Successor.ArtifactSHA256 == p.Original.ArtifactSHA256 {
		return errors.New("updater: recovery requires an independent successor binding")
	}
	if _, _, err := s.recoveryBundle(p); err != nil {
		return err
	}
	id, err := s.opts.Process.Inspect(ctx, p.OriginalProcess.PID)
	if err != nil {
		return err
	}
	if !validIdentity(id) || id != p.OriginalProcess {
		return ErrUnknown
	}
	found, err := s.opts.Process.Find(ctx, id.LaunchID, id.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 1 || found[0] != id {
		return ErrUnknown
	}
	return nil
}

// recoveryBundle validates the version that actually migrated accepted data,
// not Current, which intentionally still points at the older committed proof.
func (s *Supervisor) recoveryBundle(p protocol.ForwardRecoveryPlan) (*release.Bundle, release.Artifact, error) {
	op := s.state.Operation
	if op == nil || op.Candidate.Protocol <= 0 || op.Candidate.StorageVersion <= 0 || op.Candidate.ReleaseID != p.Original.ReleaseID || op.Candidate.ManifestSHA256 != p.Original.ManifestSHA256 || op.Candidate.Artifact.SHA256 != p.Original.ArtifactSHA256 {
		return nil, release.Artifact{}, errors.New("updater: irreversible running candidate version is not established")
	}
	b, err := release.Load(p.ReleaseDir)
	if err != nil {
		return nil, release.Artifact{}, err
	}
	artifact, err := b.Artifact("server", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, artifact, err
	}
	if err := b.Verify(artifact); err != nil {
		return nil, artifact, err
	}
	if b.Manifest.ReleaseID != p.Successor.ReleaseID || b.ManifestSHA256 != p.Successor.ManifestSHA256 || artifact.SHA256 != p.Successor.ArtifactSHA256 || !b.Manifest.Protocol.Contains(op.Candidate.Protocol) || !b.Manifest.Storage.ServerSchema.Contains(op.Candidate.StorageVersion) {
		return nil, artifact, errors.New("updater: successor cannot read the irreversible running candidate protocol/storage")
	}
	return b, artifact, nil
}

// ResourceHandoff is an operator-issued, private, expiring receipt. The paths
// and identities are reviewed outside this installer. A paused ledger task is
// deliberately insufficient: local writers require kernel exit proof; remote exits are explicit operator attestations.
// RemoteWriterFence pins evidence reviewed by the operator on another host.
// It is an authenticated relay attestation, not a local kernel observation.
type RemoteWriterFence struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Source string `json:"source"`
}

type ResourceHandoff struct {
	WindowClaim                 string             `json:"window_claim"`
	OperatorUID                 int                `json:"operator_uid"`
	ExpiresAtMillis             int64              `json:"expires_at_millis"`
	WriterProcesses             []Identity         `json:"writer_processes"`
	RemoteWriterFence           *RemoteWriterFence `json:"remote_writer_fence,omitempty"`
	RunnerCoverageUntilMillis   int64              `json:"runner_coverage_until_millis"`
	PendingOperationsReconciled string             `json:"pending_operations_reconciled"`
	WorkspaceResponsibility     string             `json:"workspace_responsibility"`
	Source                      string             `json:"source"`
}

func verifyResourceEvidence(p protocol.ForwardRecoveryPlan, now time.Time) error {
	raw, err := privateFile(p.ResourceEvidence)
	if err != nil {
		return fmt.Errorf("updater: private resource handoff unavailable: %w", err)
	}
	if bytesDigest(raw) != p.ResourceSHA256 {
		return errors.New("updater: resource handoff digest changed")
	}
	var h ResourceHandoff
	if err := strictJSON(raw, &h); err != nil {
		return err
	}
	if h.WindowClaim != p.WindowClaim || h.OperatorUID != p.OperatorUID || h.ExpiresAtMillis < p.ExpiresAtMillis || h.ExpiresAtMillis <= now.UnixMilli() || h.RunnerCoverageUntilMillis < p.ExpiresAtMillis || h.PendingOperationsReconciled == "" || h.WorkspaceResponsibility == "" || h.Source == "" || len(h.WriterProcesses) == 0 && h.RemoteWriterFence == nil {
		return errors.New("updater: physical writer fence, window, runner coverage and responsibility evidence required")
	}
	if remote := h.RemoteWriterFence; remote != nil {
		if remote.Host == "" || remote.Source == "" || !release.ValidSHA(remote.SHA256) {
			return errors.New("updater: remote writer fence requires host, source and fixed digest")
		}
		evidence, err := privateFile(remote.Path)
		if err != nil {
			return fmt.Errorf("updater: remote writer fence evidence: %w", err)
		}
		if len(evidence) == 0 || bytesDigest(evidence) != remote.SHA256 {
			return errors.New("updater: remote writer fence evidence digest changed")
		}
	}
	for _, id := range h.WriterProcesses {
		// A historical writer's executable digest may be unknown after exit.
		// Kernel PID/start identity proves exit independently of that metadata;
		// live service identities still require validIdentity with a fixed hash.
		if id.PID <= 1 || id.StartTicks == 0 || id.LaunchID == "" || id.SHA256 != "" && !release.ValidSHA(id.SHA256) {
			return errors.New("updater: invalid writer fence identity")
		}
		actual, err := (LinuxProcess{}).Inspect(context.Background(), id.PID)
		if errors.Is(err, ErrNotRunning) || err == nil && actual.StartTicks != id.StartTicks {
			continue
		}
		return errors.New("updater: original writer is running or physical fence is unknown")
	}
	return nil
}

func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("updater: trailing JSON")
	}
	return nil
}

// PlanRecovery performs every current guard without altering durable state.
func (s *Supervisor) PlanRecovery(ctx context.Context, p protocol.ForwardRecoveryPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if r := s.state.Recovery; r != nil {
		if digest(r.Plan) != digest(p) {
			return errors.New("updater: conflicting recovery plan")
		}
		if r.Phase != "updater_adopted" {
			return errors.New("updater: recovery is already authorized; inspect status")
		}
		if p.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
			return errors.New("updater: recovery authorization expired")
		}
		id, err := s.opts.Process.Inspect(ctx, p.OriginalProcess.PID)
		if err != nil {
			return err
		}
		if id != p.OriginalProcess {
			return ErrUnknown
		}
		return verifyResourceEvidence(p, s.opts.Now())
	}
	return s.validateRecovery(ctx, p)
}

// AdoptRecovery is called only by the installer-journal startup path while the
// original flock is held. It does not stop or replace the retained child.
func (s *Supervisor) AdoptRecovery(ctx context.Context, p protocol.ForwardRecoveryPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if r := s.state.Recovery; r != nil {
		if digest(r.Plan) != digest(p) {
			return errors.New("updater: recovery request conflicts with adopted payload")
		}
		return nil
	}
	if err := s.validateRecovery(ctx, p); err != nil {
		return err
	}
	bundle, artifact, err := s.recoveryBundle(p)
	if err != nil {
		return err
	}
	target, err := installVersion(s.cfg.StateDir, bundle, artifact)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	epoch, err := s.opts.NewID()
	if err != nil {
		return err
	}
	s.state.Format = 2
	boot, _, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	s.state.UpdaterEpoch = epoch
	s.state.UpdaterBootID = boot
	s.state.Recovery = &ForwardRecovery{Plan: p, OriginalState: raw, OriginalProof: s.state.CommittedProof, Target: target, Phase: "updater_adopted"}
	return s.save()
}

// AuthorizeRecovery records a new local authorization. Old grants cannot reach
// this branch, and duplicate requests cannot renew the deadline.
func (s *Supervisor) AuthorizeRecovery(ctx context.Context, p protocol.ForwardRecoveryPlan, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	r := s.state.Recovery
	if r == nil || digest(r.Plan) != digest(p) {
		return errors.New("updater: unknown recovery or conflicting request")
	}
	if r.Phase != "updater_adopted" {
		return nil
	}
	if generation != s.state.Generation {
		return errors.New("updater: stale recovery generation")
	}
	if p.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
		return errors.New("updater: recovery authorization expired")
	}
	if err := verifyResourceEvidence(p, s.opts.Now()); err != nil {
		return err
	}
	id, err := s.opts.Process.Inspect(ctx, p.OriginalProcess.PID)
	if err != nil {
		return err
	}
	if id != p.OriginalProcess {
		return ErrUnknown
	}
	boot, monotonic, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	remaining := p.ExpiresAtMillis - s.opts.Now().UnixMilli()
	if remaining <= 0 || remaining > (30*time.Minute).Milliseconds() {
		return errors.New("updater: recovery authorization duration invalid")
	}
	r.AuthorizationBootID = boot
	r.AuthorizationDeadlineMillis = monotonic + remaining
	r.Phase = "authorized"
	return s.save()
}

func (s *Supervisor) recoveryAuthorizationValid(r *ForwardRecovery) error {
	boot, monotonic, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	if boot != r.AuthorizationBootID || r.AuthorizationBootID == "" || monotonic >= r.AuthorizationDeadlineMillis || s.opts.Now().UnixMilli() >= r.Plan.ExpiresAtMillis {
		return errors.New("updater: recovery authorization expired or belongs to another boot")
	}
	return nil
}

func (s *Supervisor) recoveryStep(ctx context.Context) error {
	r := s.state.Recovery
	if r.Phase == "updater_adopted" || r.Phase == "ledger_linked" || r.Phase == "successor_committed" {
		return nil
	}
	switch r.Phase {
	case "authorized":
		if err := s.recoveryAuthorizationValid(r); err != nil {
			return err
		}
		if err := verifyResourceEvidence(r.Plan, s.opts.Now()); err != nil {
			return err
		}
		id, err := s.opts.Process.Inspect(ctx, r.Plan.OriginalProcess.PID)
		if err != nil {
			return err
		}
		if id != r.Plan.OriginalProcess {
			return ErrUnknown
		}
		r.Phase = "stopping_old"
		if err := s.save(); err != nil {
			return err
		}
		// Intent is durable before the single controlled signal. On reopen the
		// stopping_old branch observes exit; it never sends a second signal.
		if err := s.opts.Process.Stop(ctx, id, time.Duration(s.cfg.StopTimeoutSeconds)*time.Second); err != nil {
			return err
		}
		return s.recoveryOldExited(ctx, r)
	case "stopping_old":
		return s.recoveryOldExited(ctx, r)
	case "old_exited":
		if err := s.recoveryAuthorizationValid(r); err != nil {
			return err
		}
		if err := s.oldGone(ctx, r); err != nil {
			return err
		}
		if sum, err := hashFile(r.Target.Binary); err != nil || sum != r.Target.Artifact.SHA256 {
			return errors.New("updater: successor executable changed")
		}
		r.Phase = "starting_successor"
		if err := s.save(); err != nil {
			return err
		}
		extra := map[string]string{"TEND_UPDATER_INSTANCE_ID": s.cfg.InstanceID, "TEND_UPDATER_PROCESS_INSTANCE_ID": r.Plan.Successor.ProcessInstanceID, "TEND_UPDATER_LAUNCH_ID": r.Plan.Successor.ProcessInstanceID, "TEND_RELEASE_SHA256": r.Target.Artifact.SHA256, "TEND_UPDATER_RELEASE_ID": r.Target.ReleaseID, "TEND_FORWARD_RECOVERY_ID": r.Plan.RecoveryID, "TEND_FORWARD_RECOVERY_SOCKET": SocketPath(s.cfg)}
		id, err := s.opts.Process.Start(ctx, r.Target.Binary, s.cfg, extra)
		if err != nil {
			return err
		} // Even definitely-unsent starts require explicit reconciliation.
		return s.recoveryStarted(r, id)
	case "starting_successor":
		found, err := s.opts.Process.Find(ctx, r.Plan.Successor.ProcessInstanceID, r.Target.Artifact.SHA256)
		if err != nil {
			return err
		}
		if len(found) != 1 {
			return ErrUnknown
		}
		return s.recoveryStarted(r, found[0])
	case "checking_successor":
		return s.recoveryCheck(ctx, r)
	default:
		return errors.New("updater: invalid recovery phase")
	}
}
func (s *Supervisor) oldGone(ctx context.Context, r *ForwardRecovery) error {
	id, err := s.opts.Process.Inspect(ctx, r.Plan.OriginalProcess.PID)
	if err == nil && id.StartTicks == r.Plan.OriginalProcess.StartTicks {
		return ErrUnknown
	}
	if err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	found, err := s.opts.Process.Find(ctx, r.Plan.OriginalProcess.LaunchID, r.Plan.OriginalProcess.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 0 {
		return ErrUnknown
	}
	return nil
}
func (s *Supervisor) recoveryOldExited(ctx context.Context, r *ForwardRecovery) error {
	if err := s.oldGone(ctx, r); err != nil {
		return err
	}
	r.Phase = "old_exited"
	return s.save()
}
func (s *Supervisor) recoveryStarted(r *ForwardRecovery, id Identity) error {
	if !validIdentity(id) || id.SHA256 != r.Target.Artifact.SHA256 || id.LaunchID != r.Plan.Successor.ProcessInstanceID {
		return ErrUnknown
	}
	r.Process = id
	r.Phase = "checking_successor"
	return s.save()
}
func (s *Supervisor) recoveryCheck(ctx context.Context, r *ForwardRecovery) error {
	a, ok := s.opts.Authority.(RecoveryAuthority)
	if !ok {
		return errors.New("updater: fixed authority lacks recovery health")
	}
	h, err := a.RecoveryHealth(ctx, r.Plan)
	id, inspectErr := s.opts.Process.Inspect(ctx, r.Process.PID)
	good := err == nil && inspectErr == nil && id == r.Process && h.RecoveryID == r.Plan.RecoveryID && h.Binding == r.Plan.Successor && h.Process == r.Process && h.Ready && h.Registered && h.Fenced && len(h.Failures) == 0 && h.Epoch != "" && h.Protocol == r.Target.Protocol && h.Schema == r.Target.StorageVersion && h.Sequence > 0
	if !good || r.Epoch != "" && (h.Epoch != r.Epoch || h.Sequence <= r.LastSequence) {
		r.HealthCount = 0
		r.FirstSequence = 0
		r.LastSequence = 0
		r.Epoch = ""
		if saveErr := s.save(); saveErr != nil {
			return saveErr
		}
		return errors.New("updater: successor readiness/binding/sequence not confirmed")
	}
	r.Epoch = h.Epoch
	r.LastSequence = h.Sequence
	r.HealthCount++
	if r.HealthCount == 1 {
		r.FirstSequence = h.Sequence
		return s.save()
	}
	if s.state.CommittedProof != nil {
		s.state.ProofHistory = append(s.state.ProofHistory, *s.state.CommittedProof)
	}
	s.state.Current = r.Target
	s.state.Process = r.Process
	s.state.CommittedProof = &CommittedProof{Binding: r.Plan.Successor, Version: r.Target, Process: r.Process}
	r.Phase = "successor_committed"
	r.Receipt = &protocol.RecoveryReceipt{RecoveryID: r.Plan.RecoveryID, RequestID: r.Plan.RequestID, Original: r.Plan.Original, Successor: r.Plan.Successor, Process: r.Process, Generation: s.state.Generation + 1, Epoch: r.Epoch, FirstSequence: r.FirstSequence, SecondSequence: r.LastSequence}
	return s.save() // Current, process, proof and recovery receipt share one rename/fsync.
}

func (s *Supervisor) LinkRecovery(receipt protocol.RecoveryReceipt, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	r := s.state.Recovery
	if r == nil || r.Receipt == nil || *r.Receipt != receipt {
		return errors.New("updater: exact durable forward receipt required")
	}
	if r.Phase == "ledger_linked" {
		return nil
	}
	if r.Phase != "successor_committed" || generation != s.state.Generation {
		return errors.New("updater: stale receipt linkage generation")
	}
	r.Phase = "ledger_linked"
	r.LinkedReceiptSHA256 = digest(receipt)
	// Archive the intact original operation. The retained recovery snapshot is
	// the resolution proof; no committed/cancelled terminal is fabricated.
	if s.state.Recoveries == nil {
		s.state.Recoveries = map[string]ForwardRecovery{}
	}
	s.state.Recoveries[r.Plan.RecoveryID] = *r
	s.state.Operation = nil
	return s.save()
}
