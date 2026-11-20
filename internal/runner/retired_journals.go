package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/updater"
)

type retiredCursor struct {
	after, lastEventRun, lastStopRun              string
	archiveDone, usageDone, eventsDone, stopsDone bool
}

// An acknowledged physical exit transfers only auxiliary work. Never start a
// runner loop, claim a Run, or replay business commands from the old journal.
func (r *Runner) maintainRetiredJournals(ctx context.Context) {
	ticker := time.NewTicker(max(5*time.Second, r.cfg.RetryDelay))
	defer ticker.Stop()
	cursors := map[string]retiredCursor{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		state, err := updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
		if err != nil {
			r.cfg.Log.WarnContext(ctx, "retained journals supervisor unavailable", "err", err)
			continue
		}
		if state.InstanceID != r.cfg.HostID || state.CurrentJournal != r.cfg.JournalID {
			continue
		}
		r.mu.Lock()
		pending := r.retiredWork
		r.mu.Unlock()
		for _, id := range state.RetiredJournals {
			c := cursors[id]
			work := pending[id]
			if id == state.CurrentJournal || c.archiveDone && c.usageDone && c.eventsDone && c.stopsDone && !work.Workspace && len(work.Usage) == 0 {
				continue
			}
			c, err = r.serviceRetiredJournal(ctx, id, c, work)
			cursors[id] = c
			if err != nil {
				r.cfg.Log.WarnContext(ctx, "retained journal work pending", "journal", id, "err", err)
			}
		}
	}
}

func (r *Runner) serviceRetiredJournal(parent context.Context, id string, c retiredCursor, work protocol.RetiredJournalWork) (retiredCursor, error) {
	ctx, cancel := context.WithTimeout(parent, r.cfg.RequestTimeout)
	defer cancel()
	j, err := r.openRetiredJournal(ctx, id)
	if err != nil {
		return c, err
	}
	defer j.Close()
	cfg := r.cfg
	cfg.Journal = j
	cfg.JournalID = id
	retained := New(cfg)
	retained.c = r.c
	retained.control = protocol.RunnerControl{Mode: protocol.RunnerMaintenance}
	retained.controlLoaded = true
	r.controlMu.Lock()
	retained.runnerProtocol = r.runnerProtocol
	retained.nativeUsageSupported = r.nativeUsageSupported
	retained.usageCollectionSupported = r.usageCollectionSupported
	if len(cfg.RolloutRoots) == 0 {
		c.archiveDone = true
	}
	archivesEnabled := r.previewDeployment != "" && len(cfg.RolloutRoots) > 0
	r.controlMu.Unlock()
	var failures []error

	if work.Workspace {
		failures = append(failures, retained.maintainWorkspaces(ctx))
	}
	if retained.nativeUsageSupported && (!c.usageDone || len(work.Usage) > 0) {
		for _, command := range work.Usage {
			if err := j.ApplyUsageCollectionControl(ctx, command); err != nil {
				failures = append(failures, err)
			}
		}
		syncErr := retained.syncNativeUsage(ctx)
		failures = append(failures, syncErr)
		pending, err := j.HasUsageWork(ctx)
		c.usageDone = err == nil && syncErr == nil && !pending
		failures = append(failures, err)
	}
	if !c.archiveDone && archivesEnabled {
		c.after, c.archiveDone, err = r.continueRetiredArchive(ctx, j, c.after)
		failures = append(failures, err)
	}
	if !c.stopsDone {
		// Stop evidence is auxiliary and may outlive both the lease and the
		// original runner process. Give each retired Run its own turn.
		stopCtx, cancel := context.WithTimeout(parent, r.cfg.RequestTimeout)
		runs, err := j.PendingSessionStopRuns(stopCtx)
		if err == nil && len(runs) > 0 {
			start := sort.Search(len(runs), func(i int) bool { return runs[i].RunID > c.lastStopRun })
			as := runs[start%len(runs)]
			c.lastStopRun = as.RunID
			err = retained.flushSessionStop(stopCtx, as)
		}
		c.stopsDone = err == nil && len(runs) == 0
		cancel()
		failures = append(failures, err)
	}
	if !c.eventsDone {
		eventCtx, cancelEvents := context.WithTimeout(parent, r.cfg.RequestTimeout)
		defer cancelEvents()
		runs, err := j.PendingEventRuns(eventCtx)
		// A slow upload may consume this entire maintenance pass. Resume
		// with the next Run on the next pass, including after a failed attempt.
		start := sort.Search(len(runs), func(i int) bool { return runs[i].RunID > c.lastEventRun })
		for i := range runs {
			if eventCtx.Err() != nil {
				err = errors.Join(err, eventCtx.Err())
				break
			}
			as := runs[(start+i)%len(runs)]
			c.lastEventRun = as.RunID
			if e := retained.flushEvents(eventCtx, as); e != nil {
				err = errors.Join(err, e)
			}
		}
		if err == nil {
			_, err = j.PruneSpool(eventCtx)
		}
		c.eventsDone = err == nil
		failures = append(failures, err)
	}
	return c, errors.Join(failures...)
}

func (r *Runner) retiredJournalDir(id string) (string, error) {
	dir := r.cfg.JournalRoot
	if dir == "" || id == r.cfg.JournalID {
		return "", errors.New("retained journal root or predecessor identity missing")
	}
	if id != "" {
		if !release.ValidID(id) {
			return "", errors.New("invalid retained journal identity")
		}
		dir = filepath.Join(dir, "generations", id)
	}
	return dir, nil
}

func (r *Runner) openRetiredJournal(ctx context.Context, id string) (*journal.Journal, error) {
	dir, err := r.retiredJournalDir(id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "journal.db")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("retained journal is not a regular file")
	}
	return journal.Open(ctx, path, r.cfg.Now)
}

func (r *Runner) continueRetiredArchive(ctx context.Context, j *journal.Journal, after string) (string, bool, error) {
	work, err := j.PendingArchives(ctx, after, 1)
	if err != nil {
		return after, false, err
	}
	if len(work) == 0 {
		return "", after == "", nil
	}
	w := work[0]
	if err := r.syncJournalArchive(ctx, j, w.Assignment()); err != nil {
		return w.RunID, false, err
	}
	return w.RunID, false, j.CompleteArchive(ctx, w)
}
