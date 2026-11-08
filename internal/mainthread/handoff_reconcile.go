package mainthread

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (m *Manager) reconcileHandoff(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p protocol.ConversationHandoffPayload
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid handoff request"}
	}
	if p.PrepareOnly {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "summary preparation is retired"}
	}

	record, exists, err := m.app.GetHandoff(ctx, d.RequestID)
	if err != nil {
		return retry(err)
	}
	if !exists {
		pending, ok, err := m.app.PendingHandoff(ctx, m.bindingInstance())
		if err != nil {
			return retry(err)
		}
		if ok && pending.ID != d.RequestID {
			return conflictingHandoff()
		}
		return app.DeliveryResult{Outcome: app.Retry}
	}
	switch record.Phase {
	case "committed":
		return app.DeliveryResult{Outcome: app.Delivered}
	case "prepared", "created", "injected":
		return app.DeliveryResult{Outcome: app.Retry}
	default:
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "handoff contains an unconfirmed harness request; original mapping is preserved"}
	}
}
