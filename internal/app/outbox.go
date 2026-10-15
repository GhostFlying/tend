package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Notifier is told that new outbox messages were committed. Implementations
// must not block.
type Notifier interface {
	Notify()
}

// Delivery is one attempt to deliver an outbox message.
type Delivery struct {
	OutboxID        string
	RequestID       string
	ParentRequestID string
	Destination     string
	Operation       string
	Payload         json.RawMessage
	// Attempt counts delivery attempts, including this one.
	Attempt int64
}

// DeliveryOutcome classifies the result of a delivery attempt.
type DeliveryOutcome int

// Delivery outcomes.
const (
	// Delivered means the receiver accepted the message.
	Delivered DeliveryOutcome = iota + 1
	// Retry means the message certainly did not take effect and may be
	// sent again under the same request ID.
	Retry
	// Failed means the receiver rejected the message for good.
	Failed
	// Unknown means it cannot be established whether the message took
	// effect. It is never resent automatically.
	Unknown
)

// DeliveryResult is what a delivery attempt or a reconciliation produced.
type DeliveryResult struct {
	Outcome DeliveryOutcome
	// RetryAfter delays the next attempt of a Retry outcome.
	RetryAfter time.Duration
	// Reason explains a Failed or Unknown outcome. It is shown to the owner
	// and must not carry secrets.
	Reason string
}

func deliveryOf(m store.OutboxMessage) Delivery {
	return Delivery{
		OutboxID: m.OutboxID, RequestID: m.RequestID, ParentRequestID: m.ParentRequestID, Destination: m.Destination,
		Operation: m.Operation, Payload: json.RawMessage(m.PayloadJSON), Attempt: m.Attempts,
	}
}

// NextDelivery claims the oldest undelivered message of a destination for a
// delivery attempt. Messages of a destination are delivered strictly in
// order, so when the oldest one is not due yet, nothing is claimed and the
// time it becomes due is returned instead. A zero time means there is
// nothing to wait for.
//
// The dispatcher's state changes are system maintenance: they are not
// recorded as requests and stay idempotent through conditional updates.
func (a *App) NextDelivery(ctx context.Context, destination string) (Delivery, time.Time, bool, error) {
	var (
		d    Delivery
		wake time.Time
		ok   bool
	)
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		m, err := store.HeadOutbox(ctx, tx.Q(), destination)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if m.Status == store.OutboxSending {
			// Left over by a previous process; reconciliation decides.
			return nil
		}
		now := a.now()
		if due := time.UnixMilli(m.NextAttemptAt); due.After(now) {
			wake = due
			return nil
		}
		if err := tx.TransitionOutbox(ctx, store.OutboxTransition{
			OutboxID: m.OutboxID, From: store.OutboxPending, FromAttempts: m.Attempts,
			To: store.OutboxSending, Attempts: m.Attempts + 1, NextAttemptAt: m.NextAttemptAt, Now: now.UnixMilli(),
		}); err != nil {
			return err
		}
		m.Attempts++
		d, ok = deliveryOf(m), true
		return nil
	})
	if err != nil {
		return Delivery{}, time.Time{}, false, err
	}
	return d, wake, ok, nil
}

// InFlightDeliveries returns the messages a previous process left in the
// middle of a delivery attempt. They must be reconciled, not resent.
func (a *App) InFlightDeliveries(ctx context.Context) ([]Delivery, error) {
	var out []Delivery
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		msgs, err := store.ListOutboxByStatus(ctx, tx.Q(), store.OutboxSending)
		for _, m := range msgs {
			out = append(out, deliveryOf(m))
		}
		return err
	})
	return out, err
}

// FinishDelivery records the result of a delivery attempt. Failed and
// unknown outcomes also produce an event so they surface to the owner.
func (a *App) FinishDelivery(ctx context.Context, d Delivery, r DeliveryResult) error {
	now := a.now()
	tr := store.OutboxTransition{
		OutboxID: d.OutboxID, From: store.OutboxSending, FromAttempts: d.Attempt, Attempts: d.Attempt, Now: now.UnixMilli(),
	}
	var kind string
	switch r.Outcome {
	case Delivered:
		tr.To = store.OutboxDelivered
	case Retry:
		tr.To, tr.NextAttemptAt = store.OutboxPending, now.Add(r.RetryAfter).UnixMilli()
	case Failed:
		tr.To, kind = store.OutboxFailed, "outbox.failed"
	case Unknown:
		tr.To, kind = store.OutboxUnknown, "outbox.unknown"
	default:
		return fmt.Errorf("app: invalid delivery outcome %d", r.Outcome)
	}
	return a.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.TransitionOutbox(ctx, tr); err != nil {
			return err
		}
		if kind == "" {
			return nil
		}
		body, err := json.Marshal(map[string]any{
			"request_id": d.RequestID, "parent_request_id": d.ParentRequestID, "destination": d.Destination,
			"operation": d.Operation, "attempts": d.Attempt, "reason": r.Reason,
		})
		if err != nil {
			return fmt.Errorf("app: encode outbox event: %w", err)
		}
		_, err = tx.InsertEvent(ctx, store.EventRow{
			EventID: a.ids.New(protocol.PrefixEvent), Source: "dispatch", ObjectType: "outbox", ObjectID: d.OutboxID,
			Kind: kind, BodyJSON: string(body), RequestID: d.RequestID, CreatedAt: now.UnixMilli(),
		})
		return err
	})
}

// NativeRequestID returns the request ID of a call that arrives with a
// native identity, such as a harness tool call, allocating one the first
// time (D28). The mapping is identity bookkeeping and idempotent by the
// identity itself, so it is written in its own transaction: if the command
// later fails internally, a retry of the same call still gets the same ID.
func (a *App) NativeRequestID(ctx context.Context, source, nativeKey string) (string, error) {
	var id string
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		id, err = tx.MapNativeRequest(ctx, source, nativeKey, a.ids.New(protocol.PrefixRequest), a.now().UnixMilli())
		return err
	})
	return id, err
}
