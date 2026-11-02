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

func (c *centralUsage) sync(ctx context.Context) error {
	sources, err := c.app.LocalUsageSources(ctx, "")
	if err != nil {
		return err
	}
	cycle := c.discovery.Cycle(c.now().UnixMilli())
	for _, source := range sources {
		if source.RunID == "" && !source.WriterStopped {
			continue
		}
		var root nativeusage.Source
		var cursors map[string]nativeusage.Cursor
		if err := json.Unmarshal(source.Source, &root); err != nil {
			return err
		}
		if err := json.Unmarshal(source.Cursors, &cursors); err != nil {
			return err
		}
		files, err := cycle.Discover(ctx, root, source.WriterStopped && source.Coverage != "caught_up")
		if err != nil {
			c.log.WarnContext(ctx, "central native history pending", "run_id", source.RunID, "err", err)
			continue
		}
		if cursors == nil {
			cursors = map[string]nativeusage.Cursor{}
		}
		var samples []protocol.TokenUsageSample
		complete, failed := true, false
		for _, file := range files {
			page, err := nativeusage.Read(ctx, file, cursors[file.Path], 256-len(samples))
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
			continue
		}
		source.Coverage = "syncing"
		if complete && source.WriterStopped {
			source.Coverage = "caught_up"
		}
		source.Source, _ = json.Marshal(root)
		source.Cursors, _ = json.Marshal(cursors)
		if err := c.app.CommitLocalUsage(ctx, source, samples); err != nil {
			c.log.WarnContext(ctx, "central native commit pending", "run_id", source.RunID, "err", err)
		}
	}
	return nil
}

func (c *centralUsage) run(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		if err := c.sync(ctx); err != nil && ctx.Err() == nil {
			c.log.WarnContext(ctx, "central native synchronization pending", "err", err)
		}
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = c.sync(final)
			cancel()
			return
		case <-timer.C:
		}
	}
}
