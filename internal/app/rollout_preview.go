package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// WithLocalRolloutPreview reads existing central-worker originals through an injected boundary.
// It is selected only by the execution's frozen inventory, never by an owner-supplied path.
func WithLocalRolloutPreview(read func(context.Context, string, []protocol.RolloutPreviewSource, protocol.RolloutPreviewQuery) protocol.RolloutPreview) Option {
	return func(a *App) { a.localRolloutPreview = read }
}

// Diagnostic bodies exist only in these bounded, request-lifetime channels.
type rolloutPreviewPending struct {
	host      string
	work      protocol.RolloutPreviewWork
	delivered bool
	reply     chan protocol.RolloutPreview
}
type rolloutPreviewBroker struct {
	mu        sync.Mutex
	pending   map[string]*rolloutPreviewPending
	changed   chan struct{}
	cancelled []protocol.RolloutPreviewWork
}

func (b *rolloutPreviewBroker) signal() {
	if b.changed != nil {
		close(b.changed)
	}
	b.changed = make(chan struct{})
}

// ReadRolloutPreview never writes the ledger, including operation receipts.
func (a *App) ReadRolloutPreview(ctx context.Context, p protocol.Principal, runID string, query protocol.RolloutPreviewQuery) (protocol.RolloutPreview, error) {
	result := protocol.RolloutPreview{State: "runner_unreachable"}
	if err := allowKinds(p, protocol.PrincipalOwner); err != nil {
		return result, err
	}
	var run store.RunRow
	var runtime store.HostRuntimeRow
	var deployment string
	var sources []protocol.RolloutPreviewSource
	central := false
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		run, err = store.GetRun(ctx, tx.Q(), runID)
		if err != nil {
			return errNotFound(err, "run not found")
		}
		if err = tx.Q().QueryRowContext(ctx, `SELECT deployment_id FROM deployment_identity WHERE singleton=1`).Scan(&deployment); err != nil {
			return err
		}
		if pin, pinErr := store.GetRunExecution(ctx, tx.Q(), runID); pinErr == nil {
			inventory, err := store.GetAgentInventorySnapshot(ctx, tx.Q(), run.HostID, pin.SnapshotID, pin.PublicSHA256)
			if err != nil {
				return err
			}
			for _, native := range inventory.Projection.Runtimes {
				if native.RuntimeID == pin.Runtime.RuntimeID && native.Revision == pin.Runtime.Revision && native.Kind == "central-traex-worker" {
					central = true
				}
			}
		} else if !errors.Is(pinErr, store.ErrNotFound) {
			return pinErr
		}
		if !central {
			runtime, err = store.GetHostRuntime(ctx, tx.Q(), run.HostID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if run.Role == protocol.RoleIssueManager {
			rows, err := tx.Q().QueryContext(ctx, `SELECT DISTINCT t.thread_id,t.turn_id FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id WHERE json_extract(h.private_snapshot,'$.runner_run_id')=? AND t.turn_id IS NOT NULL AND t.turn_id<>'' ORDER BY t.request_id`, runID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var source protocol.RolloutPreviewSource
				if err = rows.Scan(&source.SessionID, &source.TurnID); err != nil {
					return err
				}
				sources = append(sources, source)
			}
			return rows.Err()
		}
		rows, err := tx.Q().QueryContext(ctx, `SELECT kind,body_json FROM run_logs WHERE run_id=? ORDER BY seq`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var kind, raw string
			if err = rows.Scan(&kind, &raw); err != nil {
				return err
			}
			raw, err = store.ResolveJSON(ctx, tx.Q(), raw)
			if err != nil {
				return err
			}
			var identity struct {
				Session string `json:"thread_id"`
				Turn    string `json:"turn_id"`
			}
			var source protocol.RolloutPreviewSource
			if kind == protocol.EventNativeIdentity {
				var native protocol.NativeIdentity
				if json.Unmarshal([]byte(raw), &native) != nil || native.SessionID == "" || native.State != "known" {
					continue
				}
				source = protocol.RolloutPreviewSource{SessionID: native.SessionID, TurnID: native.TurnID}
			} else if json.Unmarshal([]byte(raw), &identity) == nil && identity.Session != "" {
				source = protocol.RolloutPreviewSource{SessionID: identity.Session, TurnID: identity.Turn}
			} else {
				continue
			}
			key := source.SessionID + "\x00" + source.TurnID
			if !seen[key] && len(sources) < 512 {
				seen[key] = true
				sources = append(sources, source)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return result, err
	}
	if central {
		if a.localRolloutPreview == nil {
			return protocol.RolloutPreview{State: "unsupported"}, nil
		}
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		result = a.localRolloutPreview(readCtx, runID, sources, query)
		result.Origin = "central"
		if result.Page != nil {
			result.Page.RunID, result.Page.Observed = runID, run.ObservedState
		}
		return result, nil
	}
	if !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityRolloutPreview) {
		result.State = "unsupported"
		return result, nil
	}
	if !runtime.Online {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	work := protocol.RolloutPreviewWork{HostID: run.HostID, RequireTurn: run.Role == protocol.RoleIssueManager, RequestID: a.ids.New(protocol.PrefixRequest), DeploymentID: deployment, InstanceID: runtime.Runtime.InstanceID, RunID: runID, DeadlineMillis: deadline.UnixMilli(), Sources: sources, Query: query}
	pending := &rolloutPreviewPending{host: run.HostID, work: work, reply: make(chan protocol.RolloutPreview, 1)}
	b := &a.preview
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]*rolloutPreviewPending{}
	}
	if len(b.pending) >= 128 {
		b.mu.Unlock()
		result.State = "timeout"
		return result, nil
	}
	b.pending[work.RequestID] = pending
	b.signal()
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, work.RequestID)
		if pending.delivered && ctx.Err() != nil {
			cancelled := pending.work
			cancelled.Cancelled = true
			cancelled.Sources = nil
			if len(b.cancelled) >= 512 {
				b.cancelled = b.cancelled[1:]
			}
			b.cancelled = append(b.cancelled, cancelled)
		}
		b.signal()
		b.mu.Unlock()
	}()
	select {
	case result = <-pending.reply:
		result.Origin = "runner"
		if result.Page != nil {
			result.Page.RunID = runID
			result.Page.Observed = run.ObservedState
		}
		return result, nil
	case <-ctx.Done():
		b.mu.Lock()
		delivered := pending.delivered
		b.mu.Unlock()
		if delivered {
			result.State = "timeout"
		}
		return result, nil
	}
}

func (a *App) authorizePreviewRunner(ctx context.Context, p protocol.Principal, instance string) error {
	if err := allowKinds(p, protocol.PrincipalRunner); err != nil {
		return err
	}
	return a.store.Read(ctx, func(tx *store.ReadTx) error {
		runtime, err := store.GetHostRuntime(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		if runtime.Runtime.InstanceID != instance || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityRolloutPreview) {
			return protocol.Errorf(protocol.CodeForbidden, "preview runner instance is not current")
		}
		return nil
	})
}

// PollRolloutPreview is independent of leases, scheduling, and maintenance mode.
func (a *App) PollRolloutPreview(ctx context.Context, p protocol.Principal, instance string) (*protocol.RolloutPreviewWork, error) {
	if err := a.authorizePreviewRunner(ctx, p, instance); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	b := &a.preview
	for {
		if err := a.authorizePreviewRunner(ctx, p, instance); err != nil {
			return nil, err
		}
		b.mu.Lock()
		for i, cancelled := range b.cancelled {
			if cancelled.InstanceID == instance && cancelled.HostID == p.Subject {
				b.cancelled = append(b.cancelled[:i], b.cancelled[i+1:]...)
				b.mu.Unlock()
				return &cancelled, nil
			}
		}
		for _, pending := range b.pending {
			if pending.host == p.Subject && pending.work.InstanceID == instance && !pending.delivered {
				pending.delivered = true
				work := pending.work
				b.mu.Unlock()
				return &work, nil
			}
		}
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil
		case <-changed:
		}
	}
}

func (a *App) ReplyRolloutPreview(ctx context.Context, p protocol.Principal, reply protocol.RolloutPreviewReply) error {
	if err := a.authorizePreviewRunner(ctx, p, reply.InstanceID); err != nil {
		return err
	}
	encoded, err := json.Marshal(reply.Result)
	if err != nil || len(encoded) > 2<<20 || reply.Result.Page != nil && len(reply.Result.Page.Logs) > 100 || reply.Result.Content != nil && len(reply.Result.Content.Text) > 64<<10 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "preview reply exceeds read limits")
	}
	b := &a.preview
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := b.pending[reply.RequestID]
	if pending == nil {
		return nil
	}
	if pending.host != p.Subject || pending.work.InstanceID != reply.InstanceID || !pending.delivered {
		return protocol.Errorf(protocol.CodeForbidden, "preview request belongs to another runner")
	}
	select {
	case pending.reply <- reply.Result:
	default:
	}
	return nil
}
