package mainthread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type usageDiscovery struct {
	epoch int64
	at    int64
	files []nativeusage.Source
}

func (m *Manager) wakeNativeUsage() {
	m.mu.Lock()
	m.usageSettling = true
	m.usageSettleUntil = m.cfg.Now().Add(2 * time.Second)
	wake := m.usageWake
	m.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Manager) registerNativeUsage(ctx context.Context, th harness.Thread, purpose string) error {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	native, ok := th.(harness.NativeUsageThread)
	if !ok {
		return nil
	}
	src := native.UsageSource()
	if src.Path == "" {
		return nil
	}
	if selected := m.namedSelection(); selected != nil {
		src.ActorName = selected.Definition.DisplayName
		src.ActorID = selected.Identity.ExecutionEnvironmentID + "/" + selected.Identity.LocalID
		src.ActorCategories = selected.Definition.Categories
	}
	src.Purpose = purpose
	if purpose == "internal_summary" {
		m.mu.Lock()
		parent := m.thread
		m.mu.Unlock()
		if origin, ok := parent.(harness.NativeUsageThread); ok {
			source := origin.UsageSource()
			src.ParentSessionID = source.SessionID
			if src.ParentSessionID == "" {
				src.ParentSessionID = source.ThreadID
			}
		}
	}
	hash := sha256.Sum256([]byte(store.ConversationID(m.cfg.ConversationID) + "\x00" + src.Provider + "\x00" + src.Path))
	body, _ := json.Marshal(src)
	registration := app.LocalUsageSource{ID: hex.EncodeToString(hash[:]), ConversationID: store.ConversationID(m.cfg.ConversationID), Source: body}
	err := m.app.RegisterLocalUsage(ctx, registration)
	if protocol.CodeOf(err) == protocol.CodeStaleRevision {
		if observer, ok := th.(harness.NativeUsageWriterObserver); ok {
			retained, readErr := m.app.LocalUsageThreadSources(ctx, m.cfg.ConversationID, src.Provider, src.ThreadID)
			if readErr != nil {
				return readErr
			}
			for _, old := range retained {
				if old.WriterStopped || old.Collection.WriterID == "" || old.Collection.WriterID == src.WriterID {
					continue
				}
				if observer.ConfirmUsageWriterStopped(ctx, old.Collection.WriterID) != nil {
					continue
				}
				if stopErr := m.app.StopLocalUsage(ctx, m.cfg.ConversationID, src.ThreadID, old.Collection.WriterID); stopErr != nil {
					return stopErr
				}
			}
			err = m.app.RegisterLocalUsage(ctx, registration)
		}
	}
	if err != nil {
		m.cfg.Log.WarnContext(ctx, "native usage source registration pending", "err", err)
		return err
	}
	return nil
}

func (m *Manager) syncNativeUsage(ctx context.Context) { m.syncNativeUsageState(ctx, false) }

func (m *Manager) syncNativeUsageState(ctx context.Context, complete bool) time.Duration {
	m.mu.Lock()
	final := m.usageSettling && (m.active == nil || m.active.finished)
	m.mu.Unlock()
	return m.syncNativeUsagePass(ctx, complete, final)
}
func (m *Manager) syncNativeUsagePass(ctx context.Context, complete, final bool) (delay time.Duration) {
	m.usageMu.Lock()
	if m.usageRetries == nil {
		m.usageRetries = nativeusage.Retries{}
	}
	if m.usageCommits == nil {
		m.usageCommits = nativeusage.CommitQueue{}
	}
	now := m.cfg.Now().UnixMilli()
	pending := false
	paging := false
	defer m.usageMu.Unlock()
	if complete {
		if err := m.app.StopLocalUsage(ctx, store.ConversationID(m.cfg.ConversationID), "", m.usageWriterID); err != nil {
			m.cfg.Log.WarnContext(ctx, "native writer stop pending", "err", err)
			return 2 * time.Second
		}
		return 0
	}
	if final {
		m.mu.Lock()
		thread := ""
		if m.thread != nil {
			thread = m.thread.ID()
		}
		m.mu.Unlock()
		if err := m.app.SettleLocalUsage(ctx, m.cfg.ConversationID, thread); err != nil {
			return 2 * time.Second
		}
	}
	sources, err := m.app.PendingLocalUsageSources(ctx, store.ConversationID(m.cfg.ConversationID))
	if err != nil {
		m.cfg.Log.WarnContext(ctx, "could not read native usage sources", "err", err)
		return 2 * time.Second
	}
	if m.usageIndex == nil {
		m.usageIndex = nativeusage.NewDiscovery()
	}
	keys := map[string]bool{}
	for _, s := range sources {
		if !s.WriterStopped {
			keys[s.ID] = true
		}
	}
	m.usageRetries.Retain(keys)
	m.usageCommits.Retain(keys)
	discovery := m.usageIndex.Cycle(m.cfg.Now().UnixMilli())
	for _, s := range sources {
		if _, pending := m.usageCommits[s.ID]; pending {
			if !m.usageRetries.Due(s.ID, now) {
				continue
			}
			_, e := m.usageCommits.Retry(ctx, s.ID)
			if e != nil {
				m.usageRetries.Failed(s.ID, now)
			} else {
				m.usageRetries.Succeeded(s.ID)
				paging = true
			}
			continue
		}
		m.usageRetries.Collection(s.ID, s.Collection)
		if s.WriterStopped || strings.HasPrefix(s.Collection.RequestID, "reconcile/") || !s.Collection.Due(now) {
			continue
		}
		if s.Collection.RetryDeadlineAt != 0 && now >= s.Collection.RetryDeadlineAt {
			if e := m.failUsageSource(ctx, &s, s.Collection.ErrorCode); e != nil {
				return 2 * time.Second
			}
			m.usageRetries.Collection(s.ID, s.Collection)
			continue
		}
		beforeSource, beforeCursors, beforeCoverage := string(s.Source), string(s.Cursors), s.Coverage
		var root nativeusage.Source
		var cursors map[string]nativeusage.Cursor
		if json.Unmarshal(s.Source, &root) != nil || json.Unmarshal(s.Cursors, &cursors) != nil {
			if e := m.failUsageSource(ctx, &s, nativeusage.CollectionError(err)); e != nil {
				return 2 * time.Second
			}
			m.usageRetries.Collection(s.ID, s.Collection)
			continue
		}
		if cursors == nil {
			cursors = map[string]nativeusage.Cursor{}
		}
		if m.usageFiles == nil {
			m.usageFiles = map[string]usageDiscovery{}
		}
		err = nil
		cached := m.usageFiles[s.ID]
		files := cached.files
		if final || cached.epoch != s.Collection.Epoch || len(files) == 0 || m.cfg.Now().UnixMilli()-cached.at >= 30000 {
			files, err = discovery.Discover(ctx, root, final || cached.epoch != s.Collection.Epoch)
			if err == nil {
				m.usageFiles[s.ID] = usageDiscovery{epoch: s.Collection.Epoch, at: m.cfg.Now().UnixMilli(), files: files}
			}
		}
		if err != nil {
			m.cfg.Log.WarnContext(ctx, "native usage history unavailable", "source_id", s.ID, "collection_epoch", s.Collection.Epoch, "err", err)
			if e := m.failUsageSource(ctx, &s, nativeusage.CollectionError(err)); e != nil {
				return 2 * time.Second
			}
			m.usageRetries.Collection(s.ID, s.Collection)
			continue
		}
		if root.SessionID == "" && len(files) > 0 {
			root.SessionID = files[0].SessionID
			s.Source, _ = json.Marshal(root)
		}
		samples := []protocol.TokenUsageSample{}
		caughtUp := true
		failed := false
		var failure error
		for _, src := range files {
			page, readErr := m.usageIndex.Read(ctx, src, cursors[src.Path], 256-len(samples), final)
			if readErr != nil {
				m.cfg.Log.WarnContext(ctx, "native usage history read pending", "source_id", s.ID, "collection_epoch", s.Collection.Epoch, "err", readErr)
				failed = true
				failure = readErr
				break
			}
			cursors[src.Path] = page.Cursor
			samples = append(samples, page.Samples...)
			caughtUp = caughtUp && !page.More && !page.Incomplete
			if len(samples) >= 256 {
				caughtUp = false
				break
			}
		}
		if failed {
			if e := m.failUsageSource(ctx, &s, nativeusage.CollectionError(failure)); e != nil {
				return 2 * time.Second
			}
			m.usageRetries.Collection(s.ID, s.Collection)
			continue
		}
		s.Coverage = "syncing"
		if s.WriterStopped && caughtUp {
			s.Coverage = "caught_up"
		}
		s.Cursors, _ = json.Marshal(cursors)
		pending = pending || !caughtUp
		paging = paging || len(samples) >= 256
		m.mu.Lock()
		idle := (m.active == nil || m.active.finished) && (m.usageSettleUntil.IsZero() || !m.cfg.Now().Before(m.usageSettleUntil))
		m.mu.Unlock()
		s.Collection.ReadSucceeded(now, len(samples) > 0 || beforeCursors != string(s.Cursors), caughtUp, false, idle)
		if len(samples) > 0 {
			s.Collection.Evidence = protocol.UsageObserved
		}
		if !caughtUp && len(samples) < 256 {
			s.Collection.Failed(now, false, "incomplete_record")
		}
		if len(samples) == 0 && beforeSource == string(s.Source) && beforeCursors == string(s.Cursors) && beforeCoverage == s.Coverage && s.Collection.Revision == s.ExpectedRevision {
			m.usageRetries.Collection(s.ID, s.Collection)
			continue
		}
		if err = m.usageCommits.Commit(ctx, s.ID, func(commitCtx context.Context) error { return m.app.CommitLocalUsage(commitCtx, s, samples) }); err != nil {
			m.usageRetries.Failed(s.ID, now)
			return m.usageRetries.Delay(now, false)
		}
		m.usageRetries.Collection(s.ID, s.Collection)

	}
	if paging {
		return time.Millisecond
	}
	return m.usageRetries.Delay(now, pending)
}

// startNativeUsage owns its goroutine and drains it before the harness closes.
// The durable source/cursor remains available after a failed flush or restart.
func (m *Manager) startNativeUsage(ctx context.Context, th harness.Thread) func() {
	m.registerNativeUsage(ctx, th, "")
	if native, ok := th.(harness.NativeUsageThread); ok {
		m.usageWriterID = native.UsageSource().WriterID
	}
	wake := make(chan struct{}, 1)
	m.mu.Lock()
	m.usageWake = wake
	m.mu.Unlock()
	pollCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); m.nativeUsageLoop(pollCtx, wake) }()

	return func() {
		cancel()
		<-done
		flush, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		m.syncNativeUsage(flush)
	}
}

// Completion settling is a deadline, not a count of wakeups. A buffered wake
// cannot consume the obligation to inspect files flushed after the completion.
func (m *Manager) nativeUsageLoop(ctx context.Context, wake <-chan struct{}) {
	for {
		m.mu.Lock()
		active := (m.active != nil && !m.active.finished)
		settling, until := m.usageSettling, m.usageSettleUntil
		m.usageSettling = false
		m.mu.Unlock()
		due := !until.IsZero() && !m.cfg.Now().Before(until)
		delay := m.syncNativeUsagePass(ctx, false, !active && (settling || due))
		m.mu.Lock()
		active = (m.active != nil && !m.active.finished)
		if due && delay == 0 && !active && m.usageSettleUntil.Equal(until) {
			m.usageSettleUntil = time.Time{}
		}
		until = m.usageSettleUntil
		m.mu.Unlock()
		if active && (delay == 0 || delay > 2*time.Second) {
			delay = 2 * time.Second
		}
		remaining := until.Sub(m.cfg.Now())
		// A scan can itself cross the deadline. Run the now-due final scan
		// before parking; only a completed due pass may discharge it.
		if !until.IsZero() && remaining <= 0 && delay == 0 {
			continue
		}
		if remaining > 0 && (delay == 0 || remaining < delay) {
			delay = remaining
		}
		if !nativeusage.Wait(ctx, wake, delay) {
			return
		}
	}
}

func (m *Manager) beginNativeUsage(ctx context.Context, th harness.Thread, request string) error {
	native, ok := th.(harness.NativeUsageThread)
	if !ok || native.UsageSource().Path == "" {
		return nil
	}
	if err := m.registerNativeUsage(ctx, th, ""); err != nil {
		return err
	}
	return m.app.ActivateLocalUsage(ctx, m.cfg.ConversationID, th.ID(), request)
}

func (m *Manager) failUsageSource(ctx context.Context, s *app.LocalUsageSource, code string) error {
	next := *s
	next.Collection.Failed(m.cfg.Now().UnixMilli(), next.WriterStopped, code)
	if code == "identity_mismatch" {
		next.Collection.RetireUnavailable(m.cfg.Now().UnixMilli(), next.WriterStopped)
	}
	err := m.usageCommits.Commit(ctx, s.ID, func(commitCtx context.Context) error { return m.app.CommitLocalUsage(commitCtx, next, nil) })
	if err == nil {
		*s = next
	} else {
		m.usageRetries.Failed(s.ID, m.cfg.Now().UnixMilli())
	}
	return err
}
