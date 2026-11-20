package e2e

import (
	"context"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

// Block the write connection after the marked handler writes its rollout row.
// Release only after the original ACK reports a real pre-transaction timeout.
func holdManagerWriterAtACK(ctx context.Context, s *server, item string) <-chan store.WriteSource {
	recovered := make(chan store.WriteSource, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	var original store.WriteSource
	var missed bool
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
		source := o.Source
		if source.Operation != "manager.event.apply" && source.Operation != "manager.event.ack" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if source.Operation == "manager.event.apply" && o.Outcome == "ok" && original.RunID == "" {
			var marked bool
			err := s.store.Read(ctx, func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_rollout_events a JOIN manager_native_events e
					ON e.run_id=? AND e.seq=? AND json_extract(e.body_json,'$.native.ItemID')=a.item_id
					WHERE a.item_id=? AND a.kind='item_started')`, source.RunID, source.EventSequence, item).Scan(&marked)
			})
			if err != nil || !marked {
				return
			}
			original = source
			held := make(chan struct{})
			go func() {
				_ = s.store.Write(ctx, func(*store.Tx) error {
					close(held)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return nil
				})
			}()
			select {
			case <-held:
			case <-ctx.Done():
			}
			return
		}
		if source.Operation != "manager.event.ack" || source.RunID != original.RunID || source.EventSequence != original.EventSequence {
			return
		}
		if o.Outcome == "deadline_exceeded" && o.Stage == "connection_wait" && !missed {
			missed = true
			close(release)
		}
		if o.Outcome == "ok" && missed {
			select {
			case recovered <- source:
			default:
			}
		}
	})(s.store)
	return recovered
}
