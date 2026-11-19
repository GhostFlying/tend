package runner

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/updater"
	"os"
	"path/filepath"
	"time"
)

// Once a process has exited, its archive cursors still own any pending upload
// identity. Resume that bounded work under the original journal's writer lock.
func (r *Runner) retiredRolloutArchives(ctx context.Context) {
	ticker := time.NewTicker(max(5*time.Second, r.cfg.RetryDelay))
	defer ticker.Stop()
	complete := map[string]bool{}
	after := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.controlMu.Lock()
		enabled := r.previewDeployment != "" && r.runnerProtocol >= 3
		r.controlMu.Unlock()
		if !enabled {
			continue
		}
		state, err := updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
		if err != nil {
			r.cfg.Log.WarnContext(ctx, "retained archive supervisor unavailable", "err", err)
			continue
		}
		if state.InstanceID != r.cfg.HostID || state.CurrentJournal != r.cfg.JournalID {
			continue
		}
		for _, id := range state.RetiredJournals {
			if complete[id] || id == state.CurrentJournal {
				continue
			}
			next, done, err := r.continueRetiredArchive(ctx, id, after[id])
			after[id] = next
			complete[id] = done
			if err != nil {
				r.cfg.Log.WarnContext(ctx, "retained native archive pending", "journal", id, "err", err)
			}
		}
	}
}

func (r *Runner) continueRetiredArchive(ctx context.Context, id, after string) (string, bool, error) {
	dir := r.cfg.JournalRoot
	if id != "" {
		if !release.ValidID(id) {
			return after, false, errors.New("invalid retained journal identity")
		}
		dir = filepath.Join(dir, "generations", id)
	}
	path := filepath.Join(dir, "journal.db")
	info, err := os.Lstat(path)
	if err != nil {
		return after, false, err
	}
	if !info.Mode().IsRegular() {
		return after, false, errors.New("retained journal is not a regular file")
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	j, err := journal.Open(ctx, path, r.cfg.Now)
	if err != nil {
		return after, false, err
	}
	defer j.Close()
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
