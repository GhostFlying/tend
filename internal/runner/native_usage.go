package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func (r *Runner) supportsNativeUsage() bool {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	return r.nativeUsageSupported
}

func (r *Runner) registerUsage(ctx context.Context, as protocol.Assignment, src nativeusage.Source) error {
	if src.Path == "" || src.ThreadID == "" {
		return nil
	}
	if src.SessionID == "" {
		p, err := nativeusage.Read(ctx, src, nativeusage.Cursor{}, 1)
		if err == nil {
			src.SessionID = p.SessionID
		}
	}
	h := sha256.Sum256([]byte(as.RunID + "\x00" + src.Provider + "\x00" + src.Path))
	source := journal.UsageSource{ID: hex.EncodeToString(h[:]), RunID: as.RunID, Source: src, Cursors: map[string]nativeusage.Cursor{}}
	if as.Manager != nil {
		source.ConversationID = as.Manager.ConversationID
	}
	if !r.supportsNativeUsage() {
		return r.cfg.Journal.RegisterUsageSource(ctx, source)
	}
	if err := r.queueUsageRegistration(ctx, source); err != nil {
		return err
	}
	r.wakeNativeUsage()
	return nil
}

func (r *Runner) queueUsageRegistration(ctx context.Context, source journal.UsageSource) error {
	if source.RegistrationQueued {
		return nil
	}
	src := source.Source
	env, err := r.newEnvelope(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.NativeUsageRegistration{RunID: source.RunID, ThreadID: src.ThreadID, SessionID: src.SessionID, Provider: src.Provider, Purpose: src.Purpose, ParentSessionID: src.ParentSessionID})
	if err != nil {
		return err
	}
	return r.cfg.Journal.QueueUsageRegistration(ctx, source, *env)
}

func (r *Runner) wakeNativeUsage() {
	select {
	case r.usageWake <- struct{}{}:
	default:
	}
}

func (r *Runner) nativeUsageLoop(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-r.usageWake:
		}
		if err := r.syncNativeUsage(ctx); err != nil && ctx.Err() == nil {
			r.cfg.Log.WarnContext(ctx, "native usage synchronization pending", "err", err)
		}
	}
}

func (r *Runner) syncNativeUsage(ctx context.Context) error {
	r.controlMu.Lock()
	supported := r.nativeUsageSupported
	r.controlMu.Unlock()
	if !supported {
		return nil
	}
	sources, err := r.cfg.Journal.UsageSources(ctx)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if err := r.queueUsageRegistration(ctx, source); err != nil {
			return err
		}
	}
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	sort.SliceStable(requests, func(i, j int) bool {
		return requests[i].Envelope.Operation == protocol.OpRunnerUsageRegister && requests[j].Envelope.Operation != protocol.OpRunnerUsageRegister
	})
	var failures []error
	blocked := map[string]bool{}
	for _, p := range requests {
		if p.Envelope.Operation != protocol.OpRunnerUsageUpload && p.Envelope.Operation != protocol.OpRunnerUsageRegister {
			continue
		}
		op, err := r.c.do(ctx, r.c.host(), &p.Envelope)
		if err != nil {
			failures = append(failures, err)
			var input protocol.UsageUpload
			if json.Unmarshal(p.Envelope.Payload, &input) == nil {
				blocked[input.RunID] = true
			}
			continue
		}
		if op.Error != nil {
			failures = append(failures, op.Error)
			var input protocol.UsageUpload
			if json.Unmarshal(p.Envelope.Payload, &input) == nil {
				blocked[input.RunID] = true
			}
			continue
		}
		if op.Status != protocol.OpSucceeded || op.RequestID != p.Envelope.RequestID {
			failures = append(failures, errors.New("native usage: upload receipt is not a matching committed result"))
			var input protocol.UsageUpload
			if json.Unmarshal(p.Envelope.Payload, &input) == nil {
				blocked[input.RunID] = true
			}
			continue
		}
		if err = r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
			return err
		}
	}
	sources, err = r.cfg.Journal.UsageSources(ctx)
	if err != nil {
		return err
	}
	if r.usageDiscovery == nil {
		r.usageDiscovery = nativeusage.NewDiscovery()
	}
	discovery := r.usageDiscovery.Cycle(r.cfg.Now().UnixMilli())
	for _, s := range sources {
		if blocked[s.RunID] {
			continue
		}
		if err := r.syncUsageSource(ctx, s, discovery); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Runner) syncUsageSource(ctx context.Context, s journal.UsageSource, discovery *nativeusage.DiscoveryCycle) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	run, runErr := r.cfg.Journal.HistoricalRun(ctx, s.RunID)
	stopped := s.WriterStopped || (runErr == nil && run.StoppedConfirmed)
	if len(s.Files) == 0 || r.cfg.Now().UnixMilli()-s.DiscoveredAt >= 30000 || (stopped && s.Coverage != "caught_up") {
		files, err := discovery.Discover(ctx, s.Source, stopped && s.Coverage != "caught_up")
		if err != nil {
			return err
		}
		s.Files = files
		s.DiscoveredAt = r.cfg.Now().UnixMilli()
	}
	if s.Cursors == nil {
		s.Cursors = map[string]nativeusage.Cursor{}
	}
	samples := []protocol.TokenUsageSample{}
	caughtUp := true
	for _, src := range s.Files {
		if len(samples) >= 256 {
			caughtUp = false
			break
		}
		page, err := nativeusage.Read(ctx, src, s.Cursors[src.Path], 256-len(samples))
		if err != nil {
			return err
		}
		if s.Source.SessionID == "" {
			s.Source.SessionID = page.SessionID
		}
		s.Cursors[src.Path] = page.Cursor
		samples = append(samples, page.Samples...)
		caughtUp = caughtUp && !page.More && !page.Incomplete
	}
	coverage := "syncing"
	if stopped && caughtUp {
		coverage = "caught_up"
	}
	var env *protocol.Envelope
	if len(samples) > 0 || s.Coverage != coverage {
		var err error
		env, err = r.newEnvelope(protocol.OpRunnerUsageUpload, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.UsageUpload{RunID: s.RunID, ConversationID: s.ConversationID, RootThreadID: s.Source.ThreadID, SessionID: s.Source.SessionID, Samples: samples, Coverage: coverage})
		if err != nil {
			return err
		}
	}
	s.Coverage = coverage
	if err := r.cfg.Journal.QueueUsage(ctx, s, env); err != nil {
		return fmt.Errorf("runner: persist native usage batch: %w", err)
	}
	return nil
}
