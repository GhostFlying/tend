package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/updater"
)

// loadBoundedRecovery runs before network recovery or worker admission. The
// environment value only selects a request; the fixed supervisor and actual
// process identity supply authority. A restart without the hint keeps an
// already durable quarantine and never falls through to ordinary recovery.
func (r *Runner) loadBoundedRecovery(ctx context.Context) error {
	requestID := os.Getenv("TEND_BOUNDED_RECOVERY_REQUEST_ID")
	if requestID != "" {
		if r.cfg.UpdaterControlSocket == "" || r.cfg.UpdaterInstanceID == "" {
			return errors.New("runner: bounded recovery requires the fixed updater")
		}
		startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		state, err := updater.CallSocket(startupCtx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
		if err != nil {
			return fmt.Errorf("runner: bounded recovery supervisor confirmation: %w", err)
		}
		if state.InstanceID != r.cfg.UpdaterInstanceID {
			return errors.New("runner: bounded recovery supervisor identity changed")
		}
		actual, err := (updater.LinuxProcess{}).Inspect(startupCtx, os.Getpid())
		if err != nil {
			return fmt.Errorf("runner: bounded recovery process identity: %w", err)
		}
		authority, err := updater.ValidateBoundedRecoveryStartup(state, "runner", actual, requestID)
		if err != nil {
			return fmt.Errorf("runner: bounded recovery authority: %w", err)
		}
		if authority == nil || authority.Target.InstanceID != r.cfg.UpdaterInstanceID || authority.Target.ProcessInstanceID != r.cfg.InstanceID || authority.Target.ArtifactSHA256 != r.cfg.Build.BinarySHA256 {
			return errors.New("runner: bounded recovery candidate binding changed")
		}
		if err := r.cfg.Journal.QuarantineBoundedRecovery(startupCtx, *authority); err != nil {
			return fmt.Errorf("runner: bounded recovery journal fence: %w", err)
		}
	}
	quarantine, err := r.cfg.Journal.BoundedRecovery(ctx)
	if err != nil {
		return fmt.Errorf("runner: bounded recovery journal: %w", err)
	}
	r.controlMu.Lock()
	r.boundedRecovery = quarantine
	r.controlMu.Unlock()
	return nil
}

func (r *Runner) boundedRecoveryPending() bool {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	return r.boundedRecovery != nil && r.boundedRecovery.AdmissionRelease == nil
}

func (r *Runner) boundedRunQuarantined(ctx context.Context, runID string) bool {
	protected, err := r.cfg.Journal.BoundedRunQuarantined(ctx, runID)
	return err != nil || protected
}

func (r *Runner) boundedContinuationMatches(resume *protocol.RetainedRunResume) bool {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	return r.boundedRecovery != nil && resume != nil && resume.RequestID != "" && resume.Mode == "attach" &&
		resume.RecoveryAuthorizationID == r.boundedRecovery.Authorization.RequestID
}

// replayBoundedFacts uploads only already durable factual envelopes. It never
// invokes a provider, allocates a new request, claims work or frees a resource.
// A missing acknowledgement retains the exact original envelope for the next
// bounded pass; the supervisor's durable maintenance deadline is unaffected.
func (r *Runner) replayBoundedFacts(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return
	}
	// Physical stop must reach the server before a business close reply that
	// may await event application, just as in ordinary manager recovery.
	rank := func(operation string) int {
		switch operation {
		case protocol.OpManagerNativeEvent:
			return 0
		case protocol.OpManagerNativeStopped:
			return 1
		case protocol.OpManagerNativeReply:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(requests, func(i, j int) bool {
		if requests[i].RunID != requests[j].RunID {
			return requests[i].RunID < requests[j].RunID
		}
		ir, jr := rank(requests[i].Envelope.Operation), rank(requests[j].Envelope.Operation)
		if ir == 0 && jr == 0 {
			var ie, je protocol.ManagerNativeEvent
			_ = json.Unmarshal(requests[i].Envelope.Payload, &ie)
			_ = json.Unmarshal(requests[j].Envelope.Payload, &je)
			return ie.Sequence < je.Sequence
		}
		return ir < jr
	})
	blocked := map[string]bool{}
	for _, request := range requests {
		if ctx.Err() != nil || request.RunID == "" || blocked[request.RunID] {
			continue
		}
		protected, err := r.cfg.Journal.BoundedRequestQuarantined(ctx, request.Envelope.RequestID)
		if err != nil || !protected {
			continue
		}
		run, err := r.cfg.Journal.Run(ctx, request.RunID)
		if err != nil || !boundedFactualRequest(run, request) {
			continue
		}
		op, err := r.sendOnce(ctx, runCredential(run.Assignment), &request.Envelope)
		if err != nil || op.RequestID != request.Envelope.RequestID || op.Status != protocol.OpSucceeded {
			blocked[request.RunID] = true
			r.cfg.Log.WarnContext(ctx, "bounded recovery fact remains pending", "run_id", request.RunID, "request_id", request.Envelope.RequestID, "err", err)
			continue
		}
		if request.Envelope.Operation == protocol.OpRunsSubmitArtifact || request.Envelope.Operation == protocol.OpAcceptancesSubmit {
			if err := r.cfg.Journal.AcknowledgeBoundedFinal(ctx, request, op); err != nil {
				blocked[request.RunID] = true
				r.cfg.Log.WarnContext(ctx, "bounded recovery terminal fact remains pending", "run_id", request.RunID, "request_id", request.Envelope.RequestID, "err", err)
			}
			continue
		}
		if run.Assignment.Role == protocol.RoleIssueManager && run.StoppedConfirmed && (run.Phase == journal.PhaseStarted || run.Phase == journal.PhaseReleasing) {
			terminal := request.Envelope.Operation == protocol.OpManagerNativeStopped
			if request.Envelope.Operation == protocol.OpManagerNativeReply {
				closed, err := r.cfg.Journal.CompletedManagerClose(ctx, request.RunID)
				var reply protocol.ManagerNativeReply
				terminal = err == nil && closed != nil && json.Unmarshal(request.Envelope.Payload, &reply) == nil && reply.ID == closed.ID
			}
			if terminal {
				if err := r.cfg.Journal.AcknowledgeBoundedFinal(ctx, request, op); err != nil {
					blocked[request.RunID] = true
					r.cfg.Log.WarnContext(ctx, "bounded recovery manager terminal fact remains pending", "run_id", request.RunID, "request_id", request.Envelope.RequestID, "err", err)
				}
				continue
			}
		}
		var settleErr error
		if request.Envelope.Operation == protocol.OpManagerNativeReply && (run.Assignment.Protocol >= 4 || r.supportsManagerAcknowledgements()) {
			settleErr = r.cfg.Journal.SettleManagerReceipt(ctx, request.RunID, request.Envelope)
		} else {
			settleErr = r.cfg.Journal.DeleteRequest(ctx, request.Envelope.RequestID)
		}
		if settleErr != nil {
			blocked[request.RunID] = true
		}
	}
	// This existing path sends saved message receipts only. It cannot claim a
	// message or invoke a receiver; the same pass deadline bounds all runs.
	if err := r.recoverFinalMessageReceipts(ctx); err != nil && ctx.Err() == nil {
		r.cfg.Log.WarnContext(ctx, "bounded recovery message facts remain pending", "err", err)
	}
}

func boundedFactualRequest(run journal.Run, request journal.Request) bool {
	if request.Envelope.RequestID == "" || request.Envelope.Target != runTarget(run.Assignment) {
		return false
	}
	switch request.Envelope.Operation {
	case protocol.OpManagerNativeEvent, protocol.OpManagerNativeReply:
		return run.Assignment.Role == protocol.RoleIssueManager
	case protocol.OpManagerNativeStopped:
		return run.Assignment.Role == protocol.RoleIssueManager && run.StoppedConfirmed
	case protocol.OpRunsSubmitArtifact:
		return run.Assignment.Role == protocol.RoleImplement && run.Phase == journal.PhaseSubmitting && run.StoppedConfirmed
	case protocol.OpAcceptancesSubmit:
		return run.Assignment.Role == protocol.RoleAccept && run.Phase == journal.PhaseSubmitting && run.StoppedConfirmed
	default:
		return false
	}
}
