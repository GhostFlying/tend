package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
)

// Transport owns provider IO; App owns owner identity, deduplication and outbox.
type Transport struct {
	app       *app.App
	api       *API
	callback  *Callback
	tenantKey string
	owners    []string
	socket    func(context.Context)
}

func NewTransport(a *app.App, apiConfig APIConfig, callbackConfig CallbackConfig, owners []string) (*Transport, error) {
	if a == nil || apiConfig.AppID != callbackConfig.AppID {
		return nil, errors.New("feishu: transport identity is inconsistent")
	}
	api, err := NewAPI(apiConfig)
	if err != nil {
		return nil, err
	}
	t := &Transport{app: a, api: api, tenantKey: callbackConfig.TenantKey, owners: slices.Clone(owners)}
	t.callback, err = NewCallback(callbackConfig, t)
	if err != nil {
		return nil, err
	}
	return t, nil
}
func (t *Transport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if t.callback == nil {
		http.NotFound(w, r)
		return
	}
	t.callback.ServeHTTP(w, r)
}
func (t *Transport) Receive(ctx context.Context, m Incoming) error {
	b, err := t.app.FeishuBinding(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(t.owners, b.Owner) {
		return errors.New("feishu: bound owner is no longer authorized")
	}
	op, err := t.app.ReceiveFeishu(ctx, app.FeishuIncoming{EventID: m.EventID, MessageID: m.MessageID, AppID: m.AppID, TenantKey: m.TenantKey, SenderID: m.SenderID, ChatID: m.ChatID, ParentID: m.ParentID, Text: m.Text})
	if err != nil {
		return err
	}
	if op.Error != nil {
		return op.Error
	}
	// Accepted means the input message and outbox are durable, not that a model
	// has processed them. A missing/unknown request receipt cannot be acknowledged.
	if op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
		return ErrOutcomeUnknown
	}
	return nil
}
func (t *Transport) Deliver(ctx context.Context, d app.Delivery) app.DeliveryResult {
	var p app.FeishuPart
	if json.Unmarshal(d.Payload, &p) != nil || p.DeliveryID != d.OutboxID || p.AppID != t.api.config.AppID || p.TenantKey != t.tenantKey {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "invalid_frozen_feishu_delivery"}
	}
	if t.app.ValidateFeishuPart(ctx, p) != nil {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "feishu_binding_changed"}
	}
	b, err := t.app.FeishuBinding(ctx)
	if err != nil {
		return app.DeliveryResult{Outcome: app.Retry, Reason: "feishu_binding_unavailable"}
	}
	if !slices.Contains(t.owners, b.Owner) {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "feishu_owner_revoked"}
	}
	id, err := t.api.SendText(ctx, p.ChatID, p.Body, p.DeliveryID)
	if err != nil {
		var limited *RateLimitedError
		if errors.As(err, &limited) {
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: limited.After, Reason: "feishu_rate_limited"}
		}
		if errors.Is(err, ErrBeforeSendTemporary) {
			return app.DeliveryResult{Outcome: app.Retry, RetryAfter: 5 * time.Second, Reason: "feishu_credentials_temporarily_unavailable"}
		}
		if errors.Is(err, ErrNotSent) {
			return app.DeliveryResult{Outcome: app.Failed, Reason: "feishu_not_sent"}
		}
		return app.DeliveryResult{Outcome: app.Unknown, Reason: "feishu_receipt_missing"}
	}
	return app.DeliveryResult{Outcome: app.Delivered, ExternalMessageReference: id}
}
func (t *Transport) Reconcile(context.Context, app.Delivery) app.DeliveryResult {
	// The message API has no receipt lookup by this outbox UUID. Resending is
	// not reconciliation: keep the original unknown until an explicit receipt.
	return app.DeliveryResult{Outcome: app.Unknown, Reason: "feishu_receipt_missing"}
}

// Run recovers durable callbacks that failed before their business transaction.
func (t *Transport) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if t.socket != nil {
		wg.Go(func() { t.socket(ctx) })
	}
	defer wg.Wait()
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		inputs, err := t.app.PendingFeishuInputs(ctx)
		if err == nil {
			for _, m := range inputs {
				if ctx.Err() != nil {
					return
				}
				_ = t.Receive(ctx, Incoming{EventID: m.EventID, MessageID: m.MessageID, AppID: m.AppID, TenantKey: m.TenantKey, SenderID: m.SenderID, ChatID: m.ChatID, ParentID: m.ParentID, Text: m.Text})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
