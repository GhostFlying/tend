package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type workspaceCleaner interface {
	Cleanup(context.Context, protocol.WorkspaceCleanup) error
}

func (r *Runner) workspaceLifecycle(ctx context.Context) {
	for ctx.Err() == nil {
		if err := r.maintainWorkspaces(ctx); err != nil && ctx.Err() == nil {
			r.cfg.Log.WarnContext(ctx, "workspace maintenance deferred", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		case <-r.workspaceWake:
		}
	}
}

func (r *Runner) maintainWorkspaces(ctx context.Context) (resultErr error) {
	var heldResources bool
	maintenance := r.maintenance()
	if maintenance {
		r.mu.Lock()
		safe := r.resourceReconciled && !r.resourceBusy && len(r.resourceBlockers) == 0
		r.mu.Unlock()
		if safe {
			requests, err := r.cfg.Journal.Requests(ctx)
			if err != nil {
				return err
			}
			releases, err := r.cfg.Journal.WorkspaceReleases(ctx)
			if err != nil {
				return err
			}
			if len(requests) == 0 && len(releases) == 0 {
				return nil
			}
		}
	}
	r.mu.Lock()
	r.resourceBusy = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.resourceBusy = false
		r.resourceReconciled = resultErr == nil && !heldResources
		r.resourceBlockers = nil
		if heldResources {
			r.resourceBlockers = []string{"workspace_process_unconfirmed"}
		}
		if resultErr != nil {
			r.resourceBlockers = []string{"workspace_reconciliation_failed"}
		}
		r.mu.Unlock()
	}()

	cleaner, ok := r.cfg.Workspaces.(workspaceCleaner)
	if !ok {
		return nil
	}
	var deferred []error
	blocked, reconcileErr := r.reconcileWorkspaceProcesses(ctx)
	heldResources = len(blocked) > 0
	if reconcileErr != nil {
		deferred = append(deferred, fmt.Errorf("reconcile workspace processes: %w", reconcileErr))
	}
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return errors.Join(append(deferred, err)...)
	}
	attemptedReservation := false
	releaseIntents := map[protocol.WorkspaceRelease]bool{}
	for _, p := range pending {
		switch p.Envelope.Operation {
		case protocol.OpWorkspaceRelease:
			var release protocol.WorkspaceRelease
			if err := json.Unmarshal(p.Envelope.Payload, &release); err != nil {
				deferred = append(deferred, err)
				continue
			}
			releaseIntents[release] = true
			if reconcileErr != nil || blocked[release.WorkspaceID] {
				continue
			}
			if err := r.releaseWorkspace(ctx, &p.Envelope); err != nil {
				deferred = append(deferred, fmt.Errorf("release %s: %w", release.WorkspaceID, err))
			}
		case protocol.OpWorkspaceCleanup:
			attemptedReservation = true
			if err := r.cleanupRequest(ctx, cleaner, &p.Envelope); err != nil {
				deferred = append(deferred, err)
			}
		case protocol.OpWorkspaceCleanupDone:
			if _, err := r.hostOperation(ctx, &p.Envelope); err != nil {
				deferred = append(deferred, err)
				continue
			}
			if err := r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
				deferred = append(deferred, err)
			}
		}
	}
	releases, err := r.cfg.Journal.WorkspaceReleases(ctx)
	if err != nil {
		return errors.Join(append(deferred, err)...)
	}
	for _, p := range releases {
		if reconcileErr != nil || blocked[p.WorkspaceID] || releaseIntents[p] {
			continue
		}
		r.mu.Lock()
		active := false
		for _, a := range r.active {
			if a.as.Workspace.ID == p.WorkspaceID {
				active = true
				break
			}
		}
		r.mu.Unlock()
		if active {
			continue
		}
		env, err := r.newEnvelope(protocol.OpWorkspaceRelease, protocol.Target{Type: "host", ID: r.cfg.HostID}, p)
		if err != nil {
			deferred = append(deferred, err)
			continue
		}
		if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: *env}); err != nil {
			deferred = append(deferred, err)
			continue
		}
		if err := r.releaseWorkspace(ctx, env); err != nil {
			deferred = append(deferred, fmt.Errorf("release %s: %w", p.WorkspaceID, err))
		}
	}
	// Visit at most one reservation batch per pass. Unknown authorization
	// responses retain their request identity; confirmed reservations live on
	// the server and can rotate past deferred resources on the next pass.
	if !attemptedReservation {
		r.controlMu.Lock()
		var env *protocol.Envelope
		var reserveErr error
		if r.control.Mode != protocol.RunnerMaintenance && !r.bootstrapMaintenance {
			env, reserveErr = r.newEnvelope(protocol.OpWorkspaceCleanup, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.WorkspaceCleanupRequest{Limit: 10})
			if reserveErr == nil {
				reserveErr = r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: *env})
			}
		}
		r.controlMu.Unlock()
		if reserveErr != nil {
			deferred = append(deferred, reserveErr)
		} else if env != nil {
			if err := r.cleanupRequest(ctx, cleaner, env); err != nil {
				deferred = append(deferred, err)
			}
		}
	}

	return errors.Join(deferred...)
}

func (r *Runner) cleanupRequest(ctx context.Context, cleaner workspaceCleaner, env *protocol.Envelope) error {
	op, err := r.hostOperation(ctx, env)
	if err != nil {
		return err
	}
	var result protocol.WorkspaceCleanupResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return err
	}
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	// An acknowledgement whose answer was lost already proves local cleanup
	// finished. Reuse its durable identity instead of cleaning or journaling anew.
	doneIntents := map[protocol.WorkspaceCleanupDone]protocol.Envelope{}
	for _, p := range pending {
		if p.Envelope.Operation == protocol.OpWorkspaceCleanupDone {
			var done protocol.WorkspaceCleanupDone
			if err := json.Unmarshal(p.Envelope.Payload, &done); err != nil {
				return err
			}
			doneIntents[done] = p.Envelope
		}
	}
	var deferred []error
	for _, resource := range result.Workspaces {
		key := protocol.WorkspaceCleanupDone{CleanupID: resource.CleanupID, WorkspaceID: resource.WorkspaceID}
		done, exists := doneIntents[key]
		if !exists {
			if err := cleaner.Cleanup(ctx, resource); err != nil {
				deferred = append(deferred, fmt.Errorf("cleanup %s: %w", resource.WorkspaceID, err))
				continue
			}
			created, err := r.newEnvelope(protocol.OpWorkspaceCleanupDone, protocol.Target{Type: "host", ID: r.cfg.HostID}, key)
			if err != nil {
				deferred = append(deferred, err)
				continue
			}
			done = *created
			if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: done}); err != nil {
				deferred = append(deferred, err)
				continue
			}
			doneIntents[key] = done
		}
		if _, err := r.hostOperation(ctx, &done); err != nil {
			deferred = append(deferred, fmt.Errorf("confirm cleanup %s: %w", resource.WorkspaceID, err))
			continue
		}
		if err := r.cfg.Journal.DeleteRequest(ctx, done.RequestID); err != nil {
			deferred = append(deferred, err)
		}
	}
	// The decoded authorization is durable server work, including resources
	// whose local removal failed. Only acknowledgements need local replay.
	if err := r.cfg.Journal.DeleteRequest(ctx, env.RequestID); err != nil {
		deferred = append(deferred, err)
	}
	return errors.Join(deferred...)
}

func (r *Runner) releaseWorkspace(ctx context.Context, env *protocol.Envelope) error {
	op, err := r.hostOperation(ctx, env)
	if err != nil {
		return err
	}
	if op.Error != nil {
		return op.Error
	}
	var result protocol.WorkspaceReleaseResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return err
	}
	if result.Released {
		var p protocol.WorkspaceRelease
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return err
		}
		if err := r.cfg.Journal.ForgetWorkspaceRelease(ctx, p); err != nil {
			return err
		}
	}
	return r.cfg.Journal.DeleteRequest(ctx, env.RequestID)
}

func (r *Runner) hostOperation(ctx context.Context, env *protocol.Envelope) (protocol.Operation, error) {
	op, err := r.c.do(ctx, r.c.host(), env)
	if err == nil && op.Error != nil {
		return op, op.Error
	}
	return op, err
}
