// Package messaging contains transport-side communication delivery contracts.
package messaging

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/app"
)

// ReadMarker marks witnessed consumption, never transport receipt. Adapters
// declare message or contiguous-cursor semantics when registering with app.
type ReadMarker interface {
	MarkRead(context.Context, app.MessagingPart) app.DeliveryResult
}

// ReadHandler runs on a separate outbox destination from text/media delivery.
type ReadHandler struct {
	App     *app.App
	Adapter ReadMarker
}

func (h ReadHandler) Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p app.MessagingPart
	if json.Unmarshal(d.Payload, &p) != nil || p.Kind != "read" || p.DeliveryID != d.OutboxID {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid_read_sync"}
	}
	if err := h.App.ValidateMessagingPart(ctx, p); err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "read_binding_changed"}
	}
	if h.Adapter == nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "channel_mark_read_unsupported"}
	}
	return h.Adapter.MarkRead(ctx, p)
}
func (h ReadHandler) Reconcile(ctx context.Context, d app.Delivery) app.DeliveryResult {
	// Marking an already-read exact message/cursor is idempotent, unlike sending.
	return h.Deliver(ctx, d)
}
