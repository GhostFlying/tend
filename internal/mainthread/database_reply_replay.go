package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

// databaseReplyThread deliberately implements no native lifecycle or RPC.
type databaseReplyThread struct {
	app         *app.App
	run, thread string
}

func (t *databaseReplyThread) ID() string { return t.thread }
func (t *databaseReplyThread) AcknowledgeReplyPrefix(ctx context.Context, event harness.Event, consumed []int64) error {
	return t.app.AcknowledgeManagerReplyPrefix(ctx, t.run, event.CoordinatorSequence, t.thread, event.TurnID, event.ItemID, event.Text, consumed)
}

func (p *BackgroundPool) startDatabaseReplyReplay(ctx context.Context, id string, wg *sync.WaitGroup) (bool, error) {
	replay, err := p.App.LapsedManagerReplyReplay(ctx, id)
	if err != nil || replay == nil {
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active[id] != nil {
		return true, nil
	}
	if paused, err := p.App.UpgradePausesMain(ctx); err != nil {
		return false, err
	} else if paused {
		preparation, err := p.App.ArchivePreparation(ctx)
		if err != nil {
			return false, err
		}
		needed, err := p.App.ArchiveNeedsReceiptRead(ctx, id)
		if err != nil {
			return false, err
		}
		parkRead, err := p.App.UpgradeParkReceiptRead(ctx, id, replay.RunID)
		if err != nil || !(preparation.Prepared && needed || parkRead) {
			return true, err
		}
	}
	m, err := p.New(id)
	if err != nil {
		return false, err
	}
	m.archiveStarting = true
	p.active[id], p.remote[id] = m, true
	if p.database == nil {
		p.database = map[string]bool{}
	}
	p.database[id] = true
	wg.Go(func() {
		defer func() { p.mu.Lock(); delete(p.active, id); delete(p.remote, id); delete(p.database, id); p.mu.Unlock() }()
		defer func() { m.mu.Lock(); m.archiveStarting = false; m.mu.Unlock() }()
		if err := m.replayDatabaseReplies(ctx, replay); err != nil {
			m.cfg.Log.WarnContext(ctx, "manager database reply replay requires reconciliation", "run_id", replay.RunID, "thread_id", replay.ThreadID, "err", err)
		}
	})
	return true, nil
}

func (m *Manager) replayDatabaseReplies(ctx context.Context, replay *app.ManagerReplyReplay) error {
	m.namedMu.Lock()
	selection := replay.Selection
	m.named = &selection
	m.namedMu.Unlock()
	thread, exists, err := m.app.MainThread(ctx, m.bindingInstance())
	if err != nil {
		return err
	}
	if !exists || thread != replay.ThreadID {
		return errors.New("database replay original named thread binding differs")
	}
	return m.consumeDatabaseReplies(ctx, replay, &databaseReplyThread{app: m.app, run: replay.RunID, thread: thread})
}

func (m *Manager) consumeDatabaseReplies(ctx context.Context, replay *app.ManagerReplyReplay, thread replyThread) error {
	drafts := replyDrafts{}
	for _, retained := range replay.Events {
		if err := ctx.Err(); err != nil {
			return err
		}
		var original coordination.Event
		if err := json.Unmarshal(retained.Body, &original); err != nil {
			return err
		}
		if retained.RunID != replay.RunID || original.ThreadID != thread.ID() || original.Native == nil || original.Native.Kind != harness.EventAgentMessageDelta {
			return errors.New("database replay original reply event differs")
		}
		event := *original.Native
		event.CoordinatorSequence = retained.Sequence
		drafts.begin(event, m)
		drafts.consume(event)
	}
	// The entire fixed database slice has been consumed. This is a local read
	// boundary, never a fabricated native item/turn/history terminal event.
	return m.flushClosingReplies(ctx, thread, drafts, protocol.AssistantInterrupted)
}
