package upgradectl

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RecoveryNotification publishes a local audit notification after ledger commit.
// It never initiates another process action or contacts a person or model.
type RecoveryNotification struct {
	App interface {
		ForwardRecoveryReadiness(context.Context, string) (protocol.ForwardRecoveryLedger, int64, error)
	}
	Log *slog.Logger
}

func (n RecoveryNotification) check(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p struct {
		RecoveryID        string `json:"recovery_id"`
		OriginalUpgradeID string `json:"original_upgrade_id"`
	}
	if d.Operation != "recovery.linked" || json.Unmarshal(d.Payload, &p) != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid recovery notification"}
	}
	row, _, err := n.App.ForwardRecoveryReadiness(ctx, p.RecoveryID)
	if err != nil {
		return app.DeliveryResult{Outcome: app.Retry, Reason: "recovery ledger temporarily unavailable"}
	}
	if !row.Linked || row.Receipt == nil || row.Plan.Original.UpgradeID != p.OriginalUpgradeID {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "recovery notification lacks durable ledger linkage"}
	}
	return app.DeliveryResult{Outcome: app.Delivered}
}
func (n RecoveryNotification) Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult {
	result := n.check(ctx, d)
	if result.Outcome == app.Delivered && n.Log != nil {
		n.Log.InfoContext(ctx, "forward recovery ledger linked", "request_id", d.RequestID, "outbox_id", d.OutboxID)
	}
	return result
}
func (n RecoveryNotification) Reconcile(ctx context.Context, d app.Delivery) app.DeliveryResult {
	return n.check(ctx, d)
}
