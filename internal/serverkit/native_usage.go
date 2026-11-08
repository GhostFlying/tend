package serverkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
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
		return c.app.StopRunUsage(ctx, run)
	}
	return nil
}

func (c *centralUsage) sync(ctx context.Context) error { _, err := c.syncWork(ctx); return err }
func (c *centralUsage) syncWork(ctx context.Context) (time.Duration, error) {
	if c.retries == nil {
		c.retries = nativeusage.Retries{}
	}
	active := false
	now := c.now().UnixMilli()
	sources, err := c.app.PendingLocalUsageSources(ctx, "")
	if err != nil {
		return 2 * time.Second, err
	}
	keys := map[string]bool{}
	for _, source := range sources {
		if source.RunID != "" || source.WriterStopped {
			keys[source.ID] = true
		}
	}
	c.retries.Retain(keys)
	cycle := c.discovery.Cycle(c.now().UnixMilli())
	for _, source := range sources {
		if source.RunID == "" && !source.WriterStopped {
			continue
		}
		if !c.retries.Due(source.ID, now) {
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
			c.retries.Failed(source.ID, now)
			c.log.WarnContext(ctx, "central native history pending", "run_id", source.RunID, "err", err)
			continue
		}
		if cursors == nil {
			cursors = map[string]nativeusage.Cursor{}
		}
		var samples []protocol.TokenUsageSample
		complete, failed := true, false
		for _, file := range files {
			page, err := c.discovery.Read(ctx, file, cursors[file.Path], 256-len(samples), source.WriterStopped)
			if err != nil {
				c.log.WarnContext(ctx, "central native read pending", "run_id", source.RunID, "err", err)
				failed = true
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
			c.retries.Failed(source.ID, now)
			continue
		}
		beforeSource, beforeCursors, beforeCoverage := string(source.Source), string(source.Cursors), source.Coverage
		source.Coverage = "syncing"
		if complete && source.WriterStopped {
			source.Coverage = "caught_up"
		}
		source.Source, _ = json.Marshal(root)
		source.Cursors, _ = json.Marshal(cursors)
		if len(samples) == 0 && beforeSource == string(source.Source) && beforeCursors == string(source.Cursors) && beforeCoverage == source.Coverage {
			if !complete {
				c.retries.Failed(source.ID, now)
			} else {
				active = active || !source.WriterStopped
				c.retries.Succeeded(source.ID)
			}
			continue
		}
		if err := c.app.CommitLocalUsage(ctx, source, samples); err != nil {
			c.retries.Failed(source.ID, now)
			c.log.WarnContext(ctx, "central native commit pending", "run_id", source.RunID, "err", err)
		} else {
			c.retries.Succeeded(source.ID)
			active = active || !source.WriterStopped || !complete
		}
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
