package runner

import (
	"context"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/runner/journal"
)

type archiveSourceLock struct {
	gate chan struct{}
	refs int
}

// Shared native sessions may be bound to successive Runs. Serialize only that
// source, not all Runs, and preserve the original pending upload under its key.
func (r *Runner) lockArchiveSource(ctx context.Context, key string) (func(), error) {
	r.archiveMu.Lock()
	if r.archiveLocks == nil {
		r.archiveLocks = map[string]*archiveSourceLock{}
	}
	l := r.archiveLocks[key]
	if l == nil {
		l = &archiveSourceLock{gate: make(chan struct{}, 1)}
		r.archiveLocks[key] = l
	}
	l.refs++
	r.archiveMu.Unlock()
	drop := func() {
		r.archiveMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(r.archiveLocks, key)
		}
		r.archiveMu.Unlock()
	}
	select {
	case l.gate <- struct{}{}:
		return func() { <-l.gate; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func (r *Runner) wakeRolloutArchive() {
	select {
	case r.archiveWake <- struct{}{}:
	default:
	}
}

// rolloutArchives owns bounded auxiliary IO. Event producers never wait for it.
// The journal generations survive shutdown; the wake channel is only a hint.
// Each Run has at most one attempt; two slots isolate a failed source from other
// Runs. No model, execution slot, lease or business command is created here.
func (r *Runner) rolloutArchives(ctx context.Context) {
	type result struct {
		work journal.ArchiveWork
		err  error
	}
	done := make(chan result, 2)
	var attempts sync.WaitGroup
	defer attempts.Wait()
	active := map[string]bool{}
	next := map[string]time.Time{}
	retry := max(r.cfg.RetryDelay, 5*time.Second)
	ticker := time.NewTicker(retry)
	defer ticker.Stop()
	after := ""
	for {
		if ctx.Err() != nil {
			return
		}
		r.controlMu.Lock()
		enabled := r.previewDeployment != "" && r.runnerProtocol >= 3 && len(r.cfg.RolloutRoots) > 0
		r.controlMu.Unlock()
		if enabled && len(active) < 2 {
			readCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
			work, err := r.cfg.Journal.PendingArchives(readCtx, after, 64)
			cancel()
			if err != nil {
				r.cfg.Log.WarnContext(ctx, "read pending native archives", "err", err)
			} else {
				for _, w := range work {
					if len(active) == 2 {
						break
					}
					after = w.RunID
					if active[w.RunID] || r.cfg.Now().Before(next[w.RunID]) {
						continue
					}
					active[w.RunID] = true
					attempts.Go(func() {
						attemptCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
						defer cancel()
						err := r.syncRolloutArchive(attemptCtx, w.Assignment())
						if err == nil {
							err = r.cfg.Journal.CompleteArchive(attemptCtx, w)
						}
						select {
						case done <- result{w, err}:
						case <-ctx.Done():
						}
					})
				}
				if len(work) < 64 {
					after = ""
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-r.archiveWake:
		case <-ticker.C:
		case finished := <-done:
			delete(active, finished.work.RunID)
			if finished.err != nil {
				next[finished.work.RunID] = r.cfg.Now().Add(retry)
				r.cfg.Log.WarnContext(ctx, "native archive pending; business delivery continues", "run_id", finished.work.RunID, "archive_generation", finished.work.Generation, "retry_delay", retry, "err", finished.err)
			} else {
				delete(next, finished.work.RunID)
			}
		}
	}
}
