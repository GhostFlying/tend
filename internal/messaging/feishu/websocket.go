package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// NewWebSocketTransport uses the official SDK for authenticated outbound events.
// It exposes no inbound callback and reuses the same durable business intake.
func NewWebSocketTransport(a *app.App, config APIConfig, tenant string, owners []string) (*Transport, error) {
	return newWebSocketTransport(a, config, tenant, owners)
}

func newWebSocketTransport(a *app.App, config APIConfig, tenant string, owners []string, options ...larkws.ClientOption) (*Transport, error) {
	if a == nil || tenant == "" {
		return nil, errors.New("feishu: application and tenant are required")
	}
	api, err := NewAPI(config)
	if err != nil {
		return nil, err
	}
	t := &Transport{app: a, api: api, tenantKey: tenant, owners: append([]string(nil), owners...)}
	t.socket = func(ctx context.Context) {
		health := func(state string) {
			updateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_ = a.ObserveFeishuConnection(updateCtx, state)
		}
		handler := webSocketEventHandler(t)
		opts := []larkws.ClientOption{larkws.WithEventHandler(handler), larkws.WithLogger(quietSDKLogger{}), larkws.WithLogLevel(larkcore.LogLevelError), larkws.WithOnReady(func() { health("connected") }), larkws.WithOnReconnected(func() { health("connected") }), larkws.WithOnReconnecting(func() { health("reconnecting") }), larkws.WithOnDisconnected(func() { health("disconnected") }), larkws.WithOnError(func(error) { health("connection_failed") })}
		opts = append(opts, options...)
		client := larkws.NewClient(config.AppID, config.AppSecret, opts...)
		// Construction starts a cache cleaner even when Start fails or is skipped.
		// Terminal disposal owns that cleaner as well as connection workers.
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = client.Dispose(closeCtx)
		}()
		if err := client.Start(ctx); err != nil && ctx.Err() == nil {
			health("connection_failed")
		}
	}
	return t, nil
}

func webSocketEventHandler(t *Transport) *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		if event == nil || event.EventV2Base == nil || event.Schema != "2.0" || event.EventV2Base.Header == nil || event.EventV2Base.Header.EventType != "im.message.receive_v1" || event.EventV2Base.Header.AppID != t.api.config.AppID || event.EventV2Base.Header.TenantKey != t.tenantKey || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil || event.Event.Sender.SenderId == nil {
			return errors.New("feishu: invalid event identity")
		}
		m, s := event.Event.Message, event.Event.Sender
		var content struct {
			Text string `json:"text"`
		}
		if stringValue(s.SenderType) != "user" || stringValue(m.MessageType) != "text" || stringValue(m.ChatType) != "p2p" || json.Unmarshal([]byte(stringValue(m.Content)), &content) != nil || strings.TrimSpace(content.Text) == "" || len(content.Text) > maxCallbackBytes || event.EventV2Base.Header.EventID == "" || stringValue(m.MessageId) == "" || stringValue(m.ChatId) == "" || stringValue(s.SenderId.OpenId) == "" {
			return errors.New("feishu: unsupported message")
		}
		// Provider redelivery retains its event/message identity on timeout or loss.
		receiveCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		defer cancel()
		return t.Receive(receiveCtx, Incoming{EventID: event.EventV2Base.Header.EventID, AppID: event.EventV2Base.Header.AppID, TenantKey: event.EventV2Base.Header.TenantKey, MessageID: stringValue(m.MessageId), ChatID: stringValue(m.ChatId), SenderID: stringValue(s.SenderId.OpenId), ParentID: stringValue(m.ParentId), Text: content.Text})
	})
}
func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// SDK payloads and endpoint errors may contain sensitive data. Lifecycle health
// is exposed separately; never forward arbitrary SDK arguments into our logs.
type quietSDKLogger struct{}

func (quietSDKLogger) Debug(context.Context, ...interface{}) {}
func (quietSDKLogger) Info(context.Context, ...interface{})  {}
func (quietSDKLogger) Warn(context.Context, ...interface{})  {}
func (quietSDKLogger) Error(context.Context, ...interface{}) {}
