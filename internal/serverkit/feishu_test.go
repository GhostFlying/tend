package serverkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

type feishuRouteFixture struct{ calls int }

func (f *feishuRouteFixture) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.calls++
	w.WriteHeader(http.StatusUnauthorized)
}
func (*feishuRouteFixture) Deliver(context.Context, app.Delivery) app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Unknown}
}
func (*feishuRouteFixture) Reconcile(context.Context, app.Delivery) app.DeliveryResult {
	return app.DeliveryResult{Outcome: app.Unknown}
}
func (*feishuRouteFixture) Run(context.Context) {}
func TestFeishuCallbackRouteDoesNotReplaceOwnerAuthentication(t *testing.T) {
	var ownerCalls int
	owner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ownerCalls++; w.WriteHeader(http.StatusForbidden) })
	transport := &feishuRouteFixture{}
	for _, enabled := range []bool{false, true} {
		var f feishuTransport
		if enabled {
			f = transport
		}
		handler := withFeishuCallback(owner, f)
		for _, path := range []string{"/hooks/feishu", "/api/v1/messaging/feishu"} {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
			if enabled && path == "/hooks/feishu" {
				if w.Code != 401 {
					t.Fatal(w.Code)
				}
			} else if w.Code != 403 {
				t.Fatal(w.Code)
			}
		}
	}
	if transport.calls != 1 || ownerCalls != 3 {
		t.Fatal(transport.calls, ownerCalls)
	}
}

func TestFeishuAssemblyUsesEffectiveLocalOwner(t *testing.T) {
	t.Setenv("TEND_FEISHU_ASSEMBLY_SECRET", "isolated-app-secret")
	t.Setenv("TEND_FEISHU_ASSEMBLY_TOKEN", "isolated-verify-token")
	t.Setenv("TEND_FEISHU_ASSEMBLY_KEY", "isolated-encrypt-key")
	for _, boundOwner := range []string{"local-owner", "configured-owner"} {
		t.Run(boundOwner, func(t *testing.T) {
			now := time.Unix(2000000000, 0)
			finished := errors.New("effective owner verified")
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			cfg := Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", LocalOwner: "local-owner", Owners: []string{"configured-owner"}, Feishu: &FeishuConfig{AppID: "app", TenantKey: "tenant", AppSecretEnv: "TEND_FEISHU_ASSEMBLY_SECRET", VerificationTokenEnv: "TEND_FEISHU_ASSEMBLY_TOKEN", EncryptKeyEnv: "TEND_FEISHU_ASSEMBLY_KEY"}}
			err := Run(ctx, cfg, []byte("isolated-assembly-secret"), Dependencies{
				Now: func() time.Time { return now },
				Initialize: func(ctx context.Context, rt *Runtime) error {
					raw, _ := json.Marshal(protocol.BindFeishu{AppID: "app", TenantKey: "tenant", SenderID: "person", ChatID: "chat"})
					rev := int64(1)
					cmd := app.FeishuCommand{}
					op, err := rt.App.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: boundOwner, Client: "assembly-test"}, &protocol.Envelope{RequestID: rt.IDs.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: protocol.Target{Type: "channel", ID: "feishu"}, Payload: raw, ExpectedControlRevision: &rev}, cmd)
					if err != nil {
						return err
					}
					if op.Error != nil {
						return op.Error
					}
					return nil
				},
				Background: func(ctx context.Context, rt *Runtime) error {
					body := []byte(`{"schema":"2.0","header":{"event_id":"event","event_type":"im.message.receive_v1","token":"isolated-verify-token","app_id":"app","tenant_key":"tenant"},"event":{"sender":{"sender_type":"user","sender_id":{"open_id":"person"}},"message":{"message_id":"external","chat_id":"chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"hello\"}"}}}`)
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+rt.Listeners[0].Addr().String()+"/hooks/feishu", bytes.NewReader(body))
					if err != nil {
						return err
					}
					ts := strconv.FormatInt(now.Unix(), 10)
					req.Header.Set("X-Lark-Request-Timestamp", ts)
					req.Header.Set("X-Lark-Request-Nonce", "nonce")
					hash := sha256.New()
					_, _ = hash.Write([]byte(ts + "nonce" + "isolated-encrypt-key"))
					_, _ = hash.Write(body)
					req.Header.Set("X-Lark-Signature", hex.EncodeToString(hash.Sum(nil)))
					client := &http.Client{Transport: &http.Transport{}}
					defer client.CloseIdleConnections()
					response, err := client.Do(req)
					if err != nil {
						return err
					}
					defer response.Body.Close()
					want := http.StatusServiceUnavailable
					if boundOwner == cfg.LocalOwner {
						want = http.StatusOK
					}
					if response.StatusCode != want {
						return fmt.Errorf("callback status %d, want %d", response.StatusCode, want)
					}
					return finished
				},
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !errors.Is(err, finished) {
				t.Fatal(err)
			}
		})
	}
}
