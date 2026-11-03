package serverkit

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/messaging/feishu"
)

// FeishuConfig contains references to local secrets, never their values.
type FeishuConfig struct {
	ReceiveMode          string `json:"receive_mode,omitempty"`
	AppID                string `json:"app_id"`
	TenantKey            string `json:"tenant_key"`
	AppSecretEnv         string `json:"app_secret_env"`
	VerificationTokenEnv string `json:"verification_token_env"`
	EncryptKeyEnv        string `json:"encrypt_key_env"`
	// CallbackListen optionally exposes only signed callbacks behind operator TLS.
	CallbackListen string `json:"callback_listen,omitempty"`
}

type feishuTransport interface {
	http.Handler
	dispatch.Handler
	Run(context.Context)
}

func prepareFeishu(ctx context.Context, cfg *FeishuConfig, a *app.App, now func() time.Time, owners []string) (feishuTransport, error) {
	if cfg == nil {
		return nil, a.ConfigureFeishuRuntime(ctx, false)
	}
	mode := cfg.ReceiveMode
	if mode == "" {
		mode = "websocket"
	}
	if cfg.AppSecretEnv == "" {
		return nil, errors.New("feishu app secret environment reference is required")
	}
	if mode == "websocket" {
		if cfg.CallbackListen != "" {
			return nil, errors.New("feishu callback listener requires http receive mode")
		}
		t, err := feishu.NewWebSocketTransport(a, feishu.APIConfig{AppID: cfg.AppID, AppSecret: os.Getenv(cfg.AppSecretEnv), Now: now}, cfg.TenantKey, owners)
		if err != nil {
			return nil, err
		}
		return t, a.ConfigureFeishuRuntime(ctx, true)
	}
	if mode != "http" {
		return nil, errors.New("feishu receive mode must be websocket or http")
	}
	if cfg.VerificationTokenEnv == "" || cfg.EncryptKeyEnv == "" {
		return nil, errors.New("feishu secret environment references are required")
	}
	t, err := feishu.NewTransport(a, feishu.APIConfig{AppID: cfg.AppID, AppSecret: os.Getenv(cfg.AppSecretEnv), Now: now}, feishu.CallbackConfig{AppID: cfg.AppID, TenantKey: cfg.TenantKey, VerificationToken: os.Getenv(cfg.VerificationTokenEnv), EncryptKey: os.Getenv(cfg.EncryptKeyEnv), Now: now}, owners)
	if err != nil {
		return nil, err
	}
	return t, a.ConfigureFeishuRuntime(ctx, true)
}

func withFeishuCallback(next http.Handler, transport feishuTransport) http.Handler {
	if transport == nil {
		return next
	}
	mux := http.NewServeMux()
	mux.Handle("/hooks/feishu", transport)
	mux.Handle("/", next)
	return mux
}
