package runner

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// resumeDelegated begins with the evidence on the sole locked journal. New
// claims, bounded maintenance and existing retained attachments reconcile via
// their own control paths.
func (r *Runner) resumeDelegated(ctx context.Context) error {
	if r.maintenance() {
		return nil
	}
	runs, err := r.cfg.Journal.Runs(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		as := run.Assignment
		if !protocol.RunnerOwnsExecution(as.Protocol) || run.Fenced || run.Held || r.boundedRunQuarantined(ctx, as.RunID) {
			continue
		}
		retained, err := r.cfg.Journal.RetainedResume(ctx, as.RunID)
		if err != nil {
			return err
		}
		if retained != nil {
			continue
		}
		desired, err := r.cfg.Journal.ReceivedDesired(ctx, as.RunID)
		if err != nil {
			return err
		}
		if desired != "" {
			as.Desired = desired
		}
		if as.Desired != protocol.DesiredRun || run.Phase == journal.PhasePausing {
			continue
		}
		// A manager owns a command stream. Until its original native process
		// can be observed, it cannot be replaced by a fresh driver.
		if as.Role == protocol.RoleIssueManager && run.Attempts > 0 && !run.StoppedConfirmed {
			continue
		}
		r.start(ctx, as, r.cfg.Now())
	}
	return nil
}

func (r *Runner) flushStartReports(ctx context.Context, as protocol.Assignment) error {
	reports, err := r.cfg.Journal.StartReports(ctx, as.RunID)
	if err != nil {
		return err
	}
	for _, env := range reports {
		op, err := r.sendOnce(ctx, runCredential(as), &env)
		if err != nil && !(env.Operation == protocol.OpRunsReportStarted && protocol.CodeOf(err) == protocol.CodeInvalidTransition) {
			return err
		}
		if err == nil && op.Status != protocol.OpSucceeded {
			return fmt.Errorf("startup receipt pending: %s", env.Operation)
		}
		if err := r.cfg.Journal.AckStartReport(ctx, as.RunID, env.RequestID); err != nil {
			return err
		}
	}
	return nil
}
