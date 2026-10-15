package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/store"
)

// The context builder's bookkeeping (D17, D18). Packets and baselines are
// system maintenance: every change is conditional on the packet's state,
// so repeating one has no further effect.

// RecordPacket stores a rendered packet before it is injected, so its
// identity and content are fixed even if injection fails.
func (a *App) RecordPacket(ctx context.Context, p store.Packet) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertPacket(ctx, p, a.now().UnixMilli())
	})
}

// ConfirmPacket records that a packet was injected and the turn after it
// started, and moves the thread's baseline to the packet's watermark. A
// packet the baseline was invalidated after, for example by a compaction
// seen while the turn was starting, no longer counts: confirming it
// changes nothing.
func (a *App) ConfirmPacket(ctx context.Context, packetID string) error {
	now := a.now().UnixMilli()
	return a.store.Write(ctx, func(tx *store.Tx) error {
		p, err := store.GetPacket(ctx, tx.Q(), packetID)
		if err != nil {
			return err
		}
		err = tx.SetPacketStatus(ctx, packetID, store.PacketBuilt, store.PacketInjected, now)
		if errors.Is(err, store.ErrStale) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("app: confirm packet %s: %w", packetID, err)
		}
		return tx.PutBaseline(ctx, store.Baseline{ThreadKey: p.ThreadKey, Epoch: p.Epoch, Watermark: p.ToSeq, LastPacketID: packetID, Valid: true}, now)
	})
}

// FailPacket records that a packet was not, or may not have been,
// injected. The baseline becomes invalid, so the next turn gets a new full
// snapshot instead of this packet again: injection has no idempotency key.
func (a *App) FailPacket(ctx context.Context, packetID string, unknown bool) error {
	now := a.now().UnixMilli()
	status := store.PacketFailed
	if unknown {
		status = store.PacketUnknown
	}
	return a.store.Write(ctx, func(tx *store.Tx) error {
		p, err := store.GetPacket(ctx, tx.Q(), packetID)
		if err != nil {
			return err
		}
		if err := tx.SetPacketStatus(ctx, packetID, store.PacketBuilt, status, now); err != nil {
			return fmt.Errorf("app: fail packet %s: %w", packetID, err)
		}
		return invalidate(ctx, tx, p.ThreadKey, now)
	})
}

// InvalidateBaseline makes a thread's next turn start from a full
// snapshot, for example after the harness compacted its history.
func (a *App) InvalidateBaseline(ctx context.Context, threadKey string) error {
	now := a.now().UnixMilli()
	return a.store.Write(ctx, func(tx *store.Tx) error { return invalidate(ctx, tx, threadKey, now) })
}

func invalidate(ctx context.Context, tx *store.Tx, threadKey string, now int64) error {
	// A packet in flight was built for the history that is no longer
	// there; it must not validate the baseline when confirmed.
	if err := tx.SettleBuiltPackets(ctx, threadKey, now); err != nil {
		return err
	}
	b, err := store.GetBaseline(ctx, tx.Q(), threadKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil // no baseline means a full snapshot anyway
	}
	if err != nil {
		return err
	}
	b.Valid = false
	return tx.PutBaseline(ctx, b, now)
}
