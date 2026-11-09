package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"

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
	source := journal.UsageSource{Collection: protocol.NewUsageCollection(src.NoDispatchProven, false), ID: hex.EncodeToString(h[:]), RunID: as.RunID, Source: src, Cursors: map[string]nativeusage.Cursor{}}
	if as.Manager != nil {
		source.ConversationID = as.Manager.ConversationID
	}
	if err := r.cfg.Journal.RegisterUsageSource(ctx, source); err != nil {
		return err
	}
	id, err := r.cfg.Journal.UsageSourceID(ctx, as.RunID, src.Provider, src.ThreadID)
	if err != nil {
		return err
	}
	source.ID = id
	if !r.supportsNativeUsage() {
		return nil
	}
	if err := r.queueUsageRegistration(ctx, source); err != nil {
		return err
	}
	r.wakeNativeUsage()
	return nil
}

func (r *Runner) activateUsage(ctx context.Context, as protocol.Assignment, src nativeusage.Source) error {
	if src.Path == "" || src.ThreadID == "" {
		return nil
	}
	id, err := r.cfg.Journal.UsageSourceID(ctx, as.RunID, src.Provider, src.ThreadID)
	if err != nil {
		return err
	}
	return r.cfg.Journal.ActivateUsageSource(ctx, id, as.RunID+"/"+src.ThreadID)
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

func (r *Runner) wakeNativeUsage() { r.cfg.Journal.WakeUsage() }
func (r *Runner) nativeUsageLoop(ctx context.Context) {
	retries := nativeusage.Retries{}
	for {
		active := false
		err := r.syncNativeUsageWithRetries(ctx, retries, &active)
		if err != nil && ctx.Err() == nil {
			r.cfg.Log.WarnContext(ctx, "native usage synchronization pending", "err", err)
		}
		active = active || (err != nil && len(retries) == 0)
		work, readErr := r.cfg.Journal.HasUsageWork(ctx)
		delay := retries.Delay(r.cfg.Now().UnixMilli(), (work && active) || readErr != nil)
		if !nativeusage.Wait(ctx, r.cfg.Journal.UsageWake(), delay) {
			return
		}
	}
}
func (r *Runner) syncNativeUsage(ctx context.Context) error {
	return r.syncNativeUsageWithRetries(ctx, nil, nil)
}
func (r *Runner) syncNativeUsageWithRetries(ctx context.Context, retries nativeusage.Retries, active *bool) error {
	r.controlMu.Lock()
	supported := r.nativeUsageSupported
	collectionSupported := r.usageCollectionSupported
	r.controlMu.Unlock()
	if !supported {
		return nil
	}
	if r.usageCommits == nil {
		r.usageCommits = nativeusage.CommitQueue{}
	}
	sources, err := r.cfg.Journal.PendingUsageSources(ctx)
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
	if retries != nil {
		keys := map[string]bool{}
		for _, s := range sources {
			keys["source/"+s.ID] = true
		}
		for _, p := range requests {
			keys["request/"+p.Envelope.RequestID] = true
		}
		retries.Retain(keys)
	}
	sort.SliceStable(requests, func(i, j int) bool {
		return requests[i].Envelope.Operation == protocol.OpRunnerUsageRegister && requests[j].Envelope.Operation != protocol.OpRunnerUsageRegister
	})
	var failures []error
	blocked := map[string]bool{}
	for _, p := range requests {
		if p.Envelope.Operation != protocol.OpRunnerUsageUpload && p.Envelope.Operation != protocol.OpRunnerUsageRegister && p.Envelope.Operation != protocol.OpRunnerUsageCollection {
			continue
		}
		if p.Envelope.Operation == protocol.OpRunnerUsageCollection && !collectionSupported {
			continue
		}
		var scope protocol.UsageUpload
		_ = json.Unmarshal(p.Envelope.Payload, &scope)
		if blocked[scope.RunID] {
			continue
		}
		key := "request/" + p.Envelope.RequestID
		if retries != nil && !retries.Due(key, r.cfg.Now().UnixMilli()) {
			var input protocol.UsageUpload
			_ = json.Unmarshal(p.Envelope.Payload, &input)
			if p.Envelope.Operation != protocol.OpRunnerUsageCollection {
				blocked[input.RunID] = true
			}
			continue
		}
		op, err := r.c.do(ctx, r.c.host(), &p.Envelope)
		if err != nil {
			if retries != nil {
				retries.Failed(key, r.cfg.Now().UnixMilli())
			}
			failures = append(failures, err)
			var input protocol.UsageUpload
			if json.Unmarshal(p.Envelope.Payload, &input) == nil {
				if p.Envelope.Operation != protocol.OpRunnerUsageCollection {
					blocked[input.RunID] = true
				}
			}
			continue
		}
		if op.Error != nil {
			if retries != nil {
				retries.Failed(key, r.cfg.Now().UnixMilli())
			}
			failures = append(failures, op.Error)
			var input protocol.UsageUpload
			if json.Unmarshal(p.Envelope.Payload, &input) == nil {
				if p.Envelope.Operation != protocol.OpRunnerUsageCollection {
					blocked[input.RunID] = true
				}
			}
			continue
		}
		if op.Status != protocol.OpSucceeded || op.RequestID != p.Envelope.RequestID {
			if retries != nil {
				retries.Failed(key, r.cfg.Now().UnixMilli())
			}
			failures = append(failures, errors.New("native usage: upload receipt is not a matching committed result"))
			var input protocol.UsageUpload
			if json.Unmarshal(p.Envelope.Payload, &input) == nil {
				if p.Envelope.Operation != protocol.OpRunnerUsageCollection {
					blocked[input.RunID] = true
				}
			}
			continue
		}
		if retries != nil {
			retries.Succeeded(key)
		}
		if err = r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
			return err
		}
	}
	sources, err = r.cfg.Journal.PendingUsageSources(ctx)
	if err != nil {
		return err
	}
	if r.usageDiscovery == nil {
		r.usageDiscovery = nativeusage.NewDiscovery()
	}
	discovery := r.usageDiscovery.Cycle(r.cfg.Now().UnixMilli())
	for _, s := range sources {
		key := "source/" + s.ID
		if blocked[s.RunID] {
			// The pending upload owns the next wake; an expired read retry must
			// not repeatedly wake a source that cannot yet advance.
			if retries != nil {
				retries.Succeeded(key)
			}
			continue
		}
		if _, pending := r.usageCommits[s.ID]; pending {
			if retries != nil && !retries.Due(key, r.cfg.Now().UnixMilli()) {
				continue
			}
			_, e := r.usageCommits.Retry(ctx, s.ID)
			if retries != nil {
				if e != nil {
					retries.Failed(key, r.cfg.Now().UnixMilli())
				} else {
					retries.Succeeded(key)
				}
			}
			if e != nil {
				failures = append(failures, e)
			} else {
				r.wakeNativeUsage()
			}
			continue
		}
		if retries != nil {
			retries.Collection(key, s.Collection)
		}
		if !s.Collection.Due(r.cfg.Now().UnixMilli()) {
			continue
		}
		if err := r.syncUsageSource(ctx, s, discovery); err != nil {
			if _, pending := r.usageCommits[s.ID]; pending {
				if retries != nil {
					retries.Failed(key, r.cfg.Now().UnixMilli())
				}
				failures = append(failures, err)
				continue
			}
			if protocol.CodeOf(err) == protocol.CodeStaleRevision {
				continue
			}
			s.Collection.Failed(r.cfg.Now().UnixMilli(), s.WriterStopped, nativeusage.CollectionError(err))
			if nativeusage.CollectionError(err) == "identity_mismatch" {
				s.Collection.RetireUnavailable(r.cfg.Now().UnixMilli(), s.WriterStopped)
			}
			if e := r.commitUsage(ctx, s, nil); e != nil {
				return e
			}
			if retries != nil {
				retries.Collection(key, s.Collection)
			}
			failures = append(failures, err)
		} else {
			if retries != nil {
				retries.Succeeded(key)
			}
			if active != nil && s.Collection.Reading() {
				*active = true
			}
		}
	}
	if collectionSupported {
		reports, err := r.cfg.Journal.PendingCollectionReports(ctx)
		if err != nil {
			return err
		}
		for _, p := range reports {
			if p.Collection.RetirementReason == "empty" && len(p.PendingRequestIDs) > 0 {
				continue
			}
			env, err := r.newEnvelope(protocol.OpRunnerUsageCollection, protocol.Target{Type: "host", ID: r.cfg.HostID}, p)
			if err != nil {
				return err
			}
			if err = r.cfg.Journal.QueueCollectionReport(ctx, p, *env); err != nil {
				if protocol.CodeOf(err) == protocol.CodeStaleRevision {
					continue
				}
				return err
			}
			if active != nil {
				*active = true
			}
		}
	}
	return errors.Join(failures...)
}

func (r *Runner) syncUsageSource(ctx context.Context, s journal.UsageSource, discovery *nativeusage.DiscoveryCycle) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.WriterStopped && s.Collection.StopProof == "" {
		s.Collection.Stopped(r.cfg.Now().UnixMilli(), "confirmed-run-writer-stop", s.Collection.Evidence == protocol.UsageNoDispatch && len(s.Cursors) == 0)
		if err := r.commitUsage(ctx, s, nil); err != nil {
			return err
		}
		return nil
	}
	if s.Collection.RetryDeadlineAt != 0 && r.cfg.Now().UnixMilli() >= s.Collection.RetryDeadlineAt {
		s.Collection.Failed(r.cfg.Now().UnixMilli(), s.WriterStopped, s.Collection.ErrorCode)
		return r.commitUsage(ctx, s, nil)
	}
	cloned := make(map[string]nativeusage.Cursor, len(s.Cursors))
	for path, cur := range s.Cursors {
		cur.Models = maps.Clone(cur.Models)
		cloned[path] = cur
	}
	s.Cursors = cloned
	before, _ := json.Marshal(s)
	stopped := s.WriterStopped
	if len(s.Files) == 0 || r.cfg.Now().UnixMilli()-s.DiscoveredAt >= 30000 || (stopped && s.Coverage != "caught_up") {
		files, err := discovery.Discover(ctx, s.Source, len(s.Files) == 0 || stopped && s.Coverage != "caught_up")
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
		page, err := r.usageDiscovery.Read(ctx, src, s.Cursors[src.Path], 256-len(samples), stopped)
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
	progress := false
	for p, cur := range s.Cursors {
		var previous journal.UsageSource
		_ = json.Unmarshal(before, &previous)
		if cur.Offset > previous.Cursors[p].Offset || cur.FileID != previous.Cursors[p].FileID {
			progress = true
			break
		}
	}
	s.Collection.ReadSucceeded(r.cfg.Now().UnixMilli(), len(samples) > 0 || progress, caughtUp, stopped, false)
	if len(samples) > 0 {
		s.Collection.Evidence = protocol.UsageObserved
	}
	if !caughtUp && len(samples) < 256 {
		s.Collection.Failed(r.cfg.Now().UnixMilli(), stopped, "incomplete_record")
	}
	after, _ := json.Marshal(s)
	if env == nil && string(before) == string(after) {
		if !caughtUp {
			return errors.New("native usage: incomplete history record remains pending")
		}
		return nil
	}
	if err := r.commitUsage(ctx, s, env); err != nil {
		return fmt.Errorf("runner: persist native usage batch: %w", err)
	}
	return nil
}

func (r *Runner) commitUsage(ctx context.Context, s journal.UsageSource, env *protocol.Envelope) error {
	if r.usageCommits == nil {
		r.usageCommits = nativeusage.CommitQueue{}
	}
	return r.usageCommits.Commit(ctx, s.ID, func(commitCtx context.Context) error { return r.cfg.Journal.QueueUsage(commitCtx, s, env) })
}
