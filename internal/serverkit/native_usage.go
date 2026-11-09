package serverkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
)

type centralUsage struct {
	app       *app.App
	now       func() time.Time
	log       *slog.Logger
	discovery *nativeusage.Discovery
	retries   nativeusage.Retries
	commits   nativeusage.CommitQueue
	sources   func(context.Context) ([]app.LocalUsageSource, error)
}

func (c *centralUsage) register(ctx context.Context, run string, src nativeusage.Source, stopped bool) error {
	hash := sha256.Sum256([]byte(run + "\x00" + src.Provider + "\x00" + src.Path))
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	if err := c.app.RegisterLocalUsage(ctx, app.LocalUsageSource{ID: hex.EncodeToString(hash[:]), RunID: run, Source: raw, WriterStopped: stopped}); err != nil {
		return err
	}
	if stopped {
		return c.app.StopRunUsage(ctx, run, src.WriterID)
	}
	return nil
}

func (c *centralUsage) sync(ctx context.Context) error { _, err := c.syncWork(ctx); return err }
func (c *centralUsage) syncWork(ctx context.Context) (time.Duration, error) {
	if c.retries == nil {
		c.retries = nativeusage.Retries{}
	}
	if c.commits == nil {
		c.commits = nativeusage.CommitQueue{}
	}
	active, paging := false, false
	now := c.now().UnixMilli()
	load := c.sources
	if load == nil {
		load = func(ctx context.Context) ([]app.LocalUsageSource, error) {
			return c.app.PendingLocalUsageSources(ctx, "")
		}
	}
	sources, err := load(ctx)
	if err != nil {
		return 2 * time.Second, err
	}
	keys := map[string]bool{}
	for _, source := range sources {
		if source.RunID != "" || source.WriterStopped || strings.HasPrefix(source.Collection.RequestID, "reconcile/") {
			keys[source.ID] = true
		}
	}
	c.retries.Retain(keys)
	c.commits.Retain(keys)
	cycle := c.discovery.Cycle(c.now().UnixMilli())
	for _, source := range sources {
		if source.RunID == "" && !source.WriterStopped && !strings.HasPrefix(source.Collection.RequestID, "reconcile/") {
			continue
		}
		if _, pending := c.commits[source.ID]; pending {
			if !c.retries.Due(source.ID, now) {
				continue
			}
			_, e := c.commits.Retry(ctx, source.ID)
			if e != nil {
				c.retries.Failed(source.ID, now)
			} else {
				c.retries.Succeeded(source.ID)
				paging = true
			}
			continue
		}
		c.retries.Collection(source.ID, source.Collection)
		if !source.Collection.Due(now) {
			continue
		}
		if source.Collection.RetryDeadlineAt != 0 && now >= source.Collection.RetryDeadlineAt {
			if err := c.failSource(ctx, &source, source.Collection.ErrorCode); err != nil {
				return 2 * time.Second, err
			}
			c.retries.Collection(source.ID, source.Collection)
			continue
		}
		var root nativeusage.Source
		var cursors map[string]nativeusage.Cursor
		if err := json.Unmarshal(source.Source, &root); err != nil {
			return 2 * time.Second, err
		}
		if err := json.Unmarshal(source.Cursors, &cursors); err != nil {
			return 2 * time.Second, err
		}
		files, err := cycle.Discover(ctx, root, source.WriterStopped && source.Coverage != "caught_up")
		if err != nil {
			if e := c.failSource(ctx, &source, nativeusage.CollectionError(err)); e != nil {
				return 2 * time.Second, e
			}
			c.retries.Collection(source.ID, source.Collection)
			c.log.WarnContext(ctx, "central native history pending", "run_id", source.RunID, "source_id", source.ID, "collection_epoch", source.Collection.Epoch, "err", err)
			continue
		}
		if cursors == nil {
			cursors = map[string]nativeusage.Cursor{}
		}
		var samples []protocol.TokenUsageSample
		complete, failed := true, false
		var failure error
		for _, file := range files {
			page, err := c.discovery.Read(ctx, file, cursors[file.Path], 256-len(samples), source.WriterStopped)
			if err != nil {
				c.log.WarnContext(ctx, "central native read pending", "run_id", source.RunID, "source_id", source.ID, "collection_epoch", source.Collection.Epoch, "err", err)
				failed = true
				failure = err
				break
			}
			cursors[file.Path] = page.Cursor
			samples = append(samples, page.Samples...)
			complete = complete && !page.More && !page.Incomplete
			if root.SessionID == "" {
				root.SessionID = page.SessionID
			}
			if len(samples) >= 256 {
				complete = false
				break
			}
		}
		if failed {
			if e := c.failSource(ctx, &source, nativeusage.CollectionError(failure)); e != nil {
				return 2 * time.Second, e
			}
			c.retries.Collection(source.ID, source.Collection)
			continue
		}
		beforeSource, beforeCursors, beforeCoverage := string(source.Source), string(source.Cursors), source.Coverage
		source.Coverage = "syncing"
		if complete && source.WriterStopped {
			source.Coverage = "caught_up"
		}
		source.Source, _ = json.Marshal(root)
		source.Cursors, _ = json.Marshal(cursors)
		source.Collection.ReadSucceeded(now, len(samples) > 0 || beforeCursors != string(source.Cursors), complete, source.WriterStopped, strings.HasPrefix(source.Collection.RequestID, "reconcile/"))
		if len(samples) > 0 {
			source.Collection.Evidence = protocol.UsageObserved
		}
		if !complete && len(samples) < 256 {
			source.Collection.Failed(now, source.WriterStopped, "incomplete_record")
		}
		if len(samples) == 0 && beforeSource == string(source.Source) && beforeCursors == string(source.Cursors) && beforeCoverage == source.Coverage && source.Collection.Revision == source.ExpectedRevision {
			active = active || !source.WriterStopped
			c.retries.Collection(source.ID, source.Collection)
			continue
		}
		if err := c.commits.Commit(ctx, source.ID, func(commitCtx context.Context) error { return c.app.CommitLocalUsage(commitCtx, source, samples) }); err != nil {
			c.retries.Failed(source.ID, now)
			return c.retries.Delay(now, false), err
		}
		c.retries.Collection(source.ID, source.Collection)
		active = active || (source.Collection.Reading() && source.Collection.NextRetryAt == 0)
		paging = paging || len(samples) >= 256

	}
	if paging {
		return time.Millisecond, nil
	}
	return c.retries.Delay(now, active), nil
}

func (c *centralUsage) run(ctx context.Context) {
	for {
		delay, err := c.syncWork(ctx)
		if err != nil && ctx.Err() == nil {
			c.log.WarnContext(ctx, "central native synchronization pending", "err", err)
		}
		if !nativeusage.Wait(ctx, c.app.LocalUsageWake(), delay) {
			return
		}
	}
}

// drain completes successful pages without a polling delay. Failures remain durable.
func (c *centralUsage) drain(ctx context.Context) {
	for ctx.Err() == nil {
		before, err := c.app.PendingLocalUsageSources(ctx, "")
		if err != nil {
			return
		}
		_, err = c.syncWork(ctx)
		if err != nil {
			return
		}
		after, err := c.app.PendingLocalUsageSources(ctx, "")
		if err != nil {
			return
		}
		b, _ := json.Marshal(before)
		a, _ := json.Marshal(after)
		if string(b) == string(a) {
			return
		}
	}
}

func (c *centralUsage) failSource(ctx context.Context, s *app.LocalUsageSource, code string) error {
	next := *s
	next.Collection.Failed(c.now().UnixMilli(), next.WriterStopped, code)
	if code == "identity_mismatch" {
		next.Collection.RetireUnavailable(c.now().UnixMilli(), next.WriterStopped)
	}
	err := c.commits.Commit(ctx, s.ID, func(commitCtx context.Context) error { return c.app.CommitLocalUsage(commitCtx, next, nil) })
	if err == nil {
		*s = next
		if !next.Collection.Reading() {
			c.log.WarnContext(ctx, "native usage collection stopped", "source_id", s.ID, "collection_epoch", s.Collection.Epoch, "state", s.Collection.State, "reason", s.Collection.RetirementReason, "error_code", s.Collection.ErrorCode)
		}
	} else {
		c.retries.Failed(s.ID, c.now().UnixMilli())
	}
	return err
}
