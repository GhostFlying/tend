package mainthread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type usageDiscovery struct {
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

func (m *Manager) registerNativeUsage(ctx context.Context, th harness.Thread, purpose string) {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	native, ok := th.(harness.NativeUsageThread)
	if !ok {
		return
	}
	src := native.UsageSource()
	if src.Path == "" {
		return
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
	if err := m.app.RegisterLocalUsage(ctx, app.LocalUsageSource{ID: hex.EncodeToString(hash[:]), ConversationID: store.ConversationID(m.cfg.ConversationID), Source: body}); err != nil {
		m.cfg.Log.WarnContext(ctx, "native usage source registration pending", "err", err)
	}
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
	now := m.cfg.Now().UnixMilli()
	pending := false
	paging := false
	defer m.usageMu.Unlock()
	if complete {
		if err := m.app.StopLocalUsage(ctx, store.ConversationID(m.cfg.ConversationID), ""); err != nil {
			m.cfg.Log.WarnContext(ctx, "native writer stop pending", "err", err)
			return 2 * time.Second
		}
		return 0
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
	discovery := m.usageIndex.Cycle(m.cfg.Now().UnixMilli())
	for _, s := range sources {
		if s.WriterStopped || !m.usageRetries.Due(s.ID, now) {
			continue
		}
		beforeSource, beforeCursors, beforeCoverage := string(s.Source), string(s.Cursors), s.Coverage
		var root nativeusage.Source
		var cursors map[string]nativeusage.Cursor
		if json.Unmarshal(s.Source, &root) != nil || json.Unmarshal(s.Cursors, &cursors) != nil {
			m.usageRetries.Failed(s.ID, now)
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
		if final || len(files) == 0 || m.cfg.Now().UnixMilli()-cached.at >= 30000 {
			files, err = discovery.Discover(ctx, root, final)
			if err == nil {
				m.usageFiles[s.ID] = usageDiscovery{at: m.cfg.Now().UnixMilli(), files: files}
			}
		}
		if err != nil {
			m.cfg.Log.WarnContext(ctx, "native usage history unavailable", "err", err)
			m.usageRetries.Failed(s.ID, now)
			continue
		}
		if root.SessionID == "" && len(files) > 0 {
			root.SessionID = files[0].SessionID
			s.Source, _ = json.Marshal(root)
		}
		samples := []protocol.TokenUsageSample{}
		caughtUp := true
		failed := false
		for _, src := range files {
			page, readErr := m.usageIndex.Read(ctx, src, cursors[src.Path], 256-len(samples), final)
			if readErr != nil {
				m.cfg.Log.WarnContext(ctx, "native usage history read pending", "err", readErr)
				failed = true
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
			m.usageRetries.Failed(s.ID, now)
			continue
		}
		s.Coverage = "syncing"
		if s.WriterStopped && caughtUp {
			s.Coverage = "caught_up"
		}
		s.Cursors, _ = json.Marshal(cursors)
		pending = pending || !caughtUp
		paging = paging || len(samples) >= 256
		if len(samples) == 0 && beforeSource == string(s.Source) && beforeCursors == string(s.Cursors) && beforeCoverage == s.Coverage {
			if !caughtUp {
				m.usageRetries.Failed(s.ID, now)
			} else {
				m.usageRetries.Succeeded(s.ID)
			}
			continue
		}
		if err = m.app.CommitLocalUsage(ctx, s, samples); err != nil {
			m.usageRetries.Failed(s.ID, now)
			m.cfg.Log.WarnContext(ctx, "native usage commit pending", "err", err)
		} else {
			m.usageRetries.Succeeded(s.ID)
		}

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
