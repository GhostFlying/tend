package app

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type managerEventACK struct {
	run                string
	sequence           int64
	thread, turn, item string
	flush              bool
}

const managerEventACKAttempt = 5 * time.Second

// AcknowledgeManagerEvent is invoked only after the server applied the event.
// Completing an item/transport flush also covers its previously buffered deltas.
func (a *App) AcknowledgeManagerEvent(ctx context.Context, run string, seq int64, thread, turn, item string, flush bool) error {
	ack := managerEventACK{run: run, sequence: seq, thread: thread, turn: turn, item: item, flush: flush}
	return a.recoverManagerEventACK(ctx, ack, false)
}

// RecoverManagerEventACK holds the original receipt while its handler stays
// finished. A retry that never reached its transaction callback rechecks the
// live authority or the original Run's transition to receipt settlement.
// Historical and close drain readers use AcknowledgeManagerEvent directly.
func (a *App) RecoverManagerEventACK(ctx context.Context, run string, seq int64, thread, turn, item string, flush bool) error {
	ack := managerEventACK{run: run, sequence: seq, thread: thread, turn: turn, item: item, flush: flush}
	return a.recoverManagerEventACK(ctx, ack, true)
}

func (a *App) recoverManagerEventACK(ctx context.Context, ack managerEventACK, live bool) error {
	const attempts = 3
	for attempt := 0; ; attempt++ {
		step, cancel := context.WithTimeout(ctx, managerEventACKAttempt)
		started, err := a.writeManagerEventACK(step, ack, live && attempt > 0)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if started && protocol.CodeOf(err) != protocol.CodeForbidden {
			readCtx, done := context.WithTimeout(ctx, managerEventACKAttempt)
			applied, readErr := a.managerEventACKApplied(readCtx, ack)
			done()
			if applied && readErr == nil {
				// A failed caller may have missed the commit's wakeups.
				a.changes.Notify()
				a.scheduling.Notify()
				return nil
			}
			return errors.Join(err, readErr)
		}
		if attempt == attempts-1 || !store.IsTransientSQLite(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Both the original event and the exact delta selection must be durable.
// A continuous cursor may be behind an independently applied event.
func (a *App) managerEventACKApplied(ctx context.Context, ack managerEventACK) (bool, error) {
	var applied bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := tx.Q().QueryRowContext(ctx, `SELECT applied FROM manager_native_events WHERE run_id=? AND seq=?`, ack.run, ack.sequence).Scan(&applied); err != nil || !applied || !ack.flush {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM manager_native_events WHERE `+managerACKDeltaPredicate+`)`, ack.deltaArgs()...).Scan(&applied)
	})
	return applied, err
}

const managerACKDeltaPredicate = `run_id=? AND seq<? AND applied=0 AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND (?='' OR json_extract(body_json,'$.thread_id')=?) AND (?='' OR json_extract(body_json,'$.native.TurnID')=?) AND (?='' OR json_extract(body_json,'$.native.ItemID')=?)`

func (ack managerEventACK) deltaArgs() []any {
	return []any{ack.run, ack.sequence, ack.thread, ack.thread, ack.turn, ack.turn, ack.item, ack.item}
}

func (a *App) writeManagerEventACK(ctx context.Context, ack managerEventACK, recheckAuthority bool) (bool, error) {
	ctx = store.WithWriteOperation(store.WithWriteEvent(ctx, ack.run, ack.sequence), "manager.event.ack")
	started, changed, scheduling := false, false, false
	err := a.write(ctx, func(tx *store.Tx) error {
		started = true
		if recheckAuthority {
			if err := a.requireManagerEventACKRetry(ctx, tx.Q(), ack); err != nil {
				return err
			}
		}
		result, err := tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq=? AND applied=0`, ack.run, ack.sequence)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = n > 0
		if ack.flush {
			result, err = tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE `+managerACKDeltaPredicate, ack.deltaArgs()...)
			if err != nil {
				return err
			}
			n, err = result.RowsAffected()
			if err != nil {
				return err
			}
			changed = changed || n > 0
		}
		if !changed {
			return nil
		}
		closing, err := managerRunClosing(ctx, tx.Q(), ack.run)
		if err != nil {
			return err
		}
		var terminal bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE run_id=? AND observed_state IN ('finished','failed'))`, ack.run).Scan(&terminal); err != nil {
			return err
		}
		scheduling = closing || terminal
		return nil
	})
	if err == nil && changed {
		a.changes.Notify()
		if scheduling {
			a.scheduling.Notify()
		}
	}
	return started, err
}

// Recheck in the same writer transaction as the ACK. While the writer was
// unavailable, this already handled event may have become part of the
// original Run's closing tail. The receipt does not resume native business.
func (a *App) requireManagerEventACKRetry(ctx context.Context, q store.Querier, ack managerEventACK) error {
	_, sequence, stopped, err := store.ManagerNativeClose(ctx, q, ack.run)
	if err != nil {
		return err
	}
	if stopped {
		if ack.sequence <= sequence {
			return nil
		}
		return protocol.Errorf(protocol.CodeForbidden, "manager event is beyond the original physical stop")
	}
	closing, err := managerRunClosing(ctx, q, ack.run)
	if err != nil || closing {
		return err
	}
	return a.requireManagerEventACKLease(ctx, q, ack.run)
}

func (a *App) requireManagerEventACKLease(ctx context.Context, q store.Querier, run string) error {
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return err
	}
	lease, err := store.GetLeaseByRun(ctx, q, run)
	if err != nil {
		return err
	}
	allowed, err := managerNativeBusinessAllowed(ctx, q, r)
	if err != nil {
		return err
	}
	if !allowed || !lease.Current(a.now().UnixMilli()) {
		return protocol.Errorf(protocol.CodeForbidden, "manager event ACK recovery is fenced")
	}
	return nil
}
