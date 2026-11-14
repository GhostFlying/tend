package upgradectl

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

// Each mutating step owns a finite local budget inside the original admission.
// Reading an expired attempt is separate: it must still allow TickUpgrade to
// record the deadline and reconcile receipts without authorizing another action.
func (c *Controller) upgradeStepContext(ctx context.Context, row store.UpgradeRow, limit time.Duration) (context.Context, context.CancelFunc) {
	now := c.cfg.Now()
	for _, deadline := range []int64{row.Deadline, row.View.OverallDeadlineMillis} {
		if deadline > 0 {
			limit = min(limit, time.UnixMilli(deadline).Sub(now))
		}
	}
	return context.WithTimeout(ctx, limit)
}

func (c *Controller) reconcileServerUpdater(ctx context.Context, row store.UpgradeRow, observed *app.ServerObservation) error {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	started := c.cfg.Now()
	status, err := updater.ObserveSelfUpdate(readCtx, c.cfg.UpdaterSocket, row.StartRequestID)
	cause := readCtx.Err()
	cancel()
	c.logUpgradeStep(ctx, row, "observe_supervisor", started, cause, err)
	if err == nil || status.Observation.Rejection != nil {
		observed.Updater = &status.Observation
	}
	now := c.cfg.Now().UnixMilli()
	if err != nil || status.Observation.Rejection != nil || row.View.Status != "running" || row.View.Phase != "server_updater" || now >= row.View.OverallDeadlineMillis || row.Deadline > 0 && now >= row.Deadline {
		return err
	}
	request := updater.SelfUpdateRequest{Target: protocol.UpdaterSelfTarget{RequestID: row.StartRequestID, ExpiresAtMillis: row.View.OverallDeadlineMillis}, UpgradeID: row.View.UpgradeID, Release: filepath.Join(c.cfg.ReleaseDir, row.View.UpgradeID), ReleaseSetSHA256: row.ReleaseSetSHA256}
	if status.Journal != nil && status.Journal.Plan.Request == request {
		return nil // The durable original dispatch is only observed.
	}
	authorizeCtx, cancel := c.upgradeStepContext(ctx, row, 5*time.Second)
	started = c.cfg.Now()
	env := &protocol.Envelope{RequestID: c.ids.New(protocol.PrefixRequest), Operation: "upgrades.authorize_supervisor", Target: protocol.Target{Type: "upgrade", ID: row.View.UpgradeID}, ExpectedControlRevision: &row.View.Revision}
	op, err := c.app.Execute(authorizeCtx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, app.AuthorizeServerUpdater{})
	if err == nil && op.Error != nil {
		err = op.Error
	}
	cause = authorizeCtx.Err()
	cancel()
	c.logUpgradeStep(ctx, row, "authorize_supervisor", started, cause, err)
	if err != nil {
		return err
	}
	prepareCtx, cancel := c.upgradeStepContext(ctx, row, updater.SelfUpdateTimeout)
	defer cancel()
	started = c.cfg.Now()
	status, err = updater.SelfUpdateCall(prepareCtx, c.cfg.UpdaterSocket, &request)
	c.logUpgradeStep(ctx, row, "prepare_supervisor", started, prepareCtx.Err(), err)
	if err == nil || status.Observation.Rejection != nil {
		observed.Updater = &status.Observation
	}
	return err
}

func (c *Controller) logUpgradeStep(ctx context.Context, row store.UpgradeRow, step string, started time.Time, cause, err error) {
	// Only the serial controller loop uses this diagnostic cache. It keeps
	// at most the current batch's step errors, never business authority/state.
	if c.loggedUpgradeID != row.View.UpgradeID {
		c.loggedUpgradeID, c.stepErrors = row.View.UpgradeID, map[string]string{}
	}
	previous := c.stepErrors[step]
	if err != nil {
		message := fmt.Sprint(err, ": ", cause)
		if message == previous {
			return
		}
		c.stepErrors[step] = message
	} else {
		delete(c.stepErrors, step)
		if step == "observe_supervisor" && previous == "" {
			return
		}
	}
	now := c.cfg.Now()
	attrs := []any{"upgrade_id", row.View.UpgradeID, "request_id", row.StartRequestID, "instance_id", row.ServerInstance, "phase", row.View.Phase, "step", step, "elapsed_ms", now.Sub(started).Milliseconds(), "phase_deadline_ms", row.Deadline, "overall_deadline_ms", row.View.OverallDeadlineMillis, "overall_remaining_ms", row.View.OverallDeadlineMillis - now.UnixMilli()}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, "error", err, "context_error", cause)
	}
	c.log.Log(ctx, level, "upgrade control step", attrs...)
}

func (c *Controller) logUpgradeTransition(ctx context.Context, before, after protocol.UpgradeView, requestID string) {
	attrs := []any{"upgrade_id", after.UpgradeID, "request_id", requestID, "revision", after.Revision, "status", after.Status, "phase", after.Phase, "previous_phase", before.Phase, "batch_elapsed_ms", c.cfg.Now().Sub(after.CreatedAt).Milliseconds(), "overall_deadline_ms", after.OverallDeadlineMillis, "reason", after.Reason}
	if before.Status != after.Status || before.Phase != after.Phase || before.Reason != after.Reason {
		c.log.InfoContext(ctx, "upgrade phase changed", attrs...)
	}
	for i, host := range after.Hosts {
		if i < len(before.Hosts) && before.Hosts[i].HostID == host.HostID && before.Hosts[i].Status == host.Status && before.Hosts[i].Phase == host.Phase && before.Hosts[i].Reason == host.Reason {
			continue
		}
		c.log.InfoContext(ctx, "upgrade host phase changed", append(attrs, "host_id", host.HostID, "host_status", host.Status, "host_phase", host.Phase, "host_reason", host.Reason)...)
	}
}
