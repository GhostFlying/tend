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
	wake := m.usageWake
	m.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Manager) registerNativeUsage(ctx context.Context, th harness.Thread, purpose string) {
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

func (m *Manager) syncNativeUsageState(ctx context.Context, complete bool) {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	if complete {
		if err := m.app.StopLocalUsage(ctx, store.ConversationID(m.cfg.ConversationID), ""); err != nil {
			m.cfg.Log.WarnContext(ctx, "native writer stop pending", "err", err)
			return
		}
	}
	sources, err := m.app.LocalUsageSources(ctx, store.ConversationID(m.cfg.ConversationID))
	if err != nil {
		m.cfg.Log.WarnContext(ctx, "could not read native usage sources", "err", err)
		return
	}
	if m.usageIndex == nil {
		m.usageIndex = nativeusage.NewDiscovery()
	}
	discovery := m.usageIndex.Cycle(m.cfg.Now().UnixMilli())
	for _, s := range sources {
		var root nativeusage.Source
		var cursors map[string]nativeusage.Cursor
		if json.Unmarshal(s.Source, &root) != nil || json.Unmarshal(s.Cursors, &cursors) != nil {
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
		if complete || len(files) == 0 || m.cfg.Now().UnixMilli()-cached.at >= 30000 {
			files, err = discovery.Discover(ctx, root, complete || (s.WriterStopped && s.Coverage != "caught_up"))
			if err == nil {
				m.usageFiles[s.ID] = usageDiscovery{at: m.cfg.Now().UnixMilli(), files: files}
			}
		}
		if err != nil {
			m.cfg.Log.WarnContext(ctx, "native usage history unavailable", "err", err)
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
			page, readErr := nativeusage.Read(ctx, src, cursors[src.Path], 256-len(samples))
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
			continue
		}
		s.Coverage = "syncing"
		if s.WriterStopped && caughtUp {
			s.Coverage = "caught_up"
		}
		s.Cursors, _ = json.Marshal(cursors)
		if err = m.app.CommitLocalUsage(ctx, s, samples); err != nil {
			m.cfg.Log.WarnContext(ctx, "native usage commit pending", "err", err)
		}

	}
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
	go func() {
		defer close(done)
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		m.syncNativeUsage(pollCtx)
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-timer.C:
				m.syncNativeUsage(pollCtx)
			case <-wake:
				m.syncNativeUsage(pollCtx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
		flush, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		m.syncNativeUsage(flush)
	}
}
