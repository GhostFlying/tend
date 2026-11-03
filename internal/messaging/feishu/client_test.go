package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendTextPreservesUUIDAndNeverRetriesUnknown(t *testing.T) {
	var auth, sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			auth.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"private-fixture","expire":7200}`)
			return
		}
		sends.Add(1)
		if r.URL.Query().Get("receive_id_type") != "chat_id" || r.Header.Get("Authorization") != "Bearer private-fixture" {
			t.Error("send identity missing")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["uuid"] != "stable-outbox" || body["receive_id"] != "fixed-chat" || body["content"] != `{"text":"hello"}` {
			t.Error("frozen send changed", body)
		}
		if sends.Load() == 1 {
			_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"opaque-receipt"}}`)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	api, err := NewAPI(APIConfig{AppID: "app", AppSecret: "secret", BaseURL: server.URL, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	id, err := api.SendText(t.Context(), "fixed-chat", "hello", "stable-outbox")
	if err != nil || id != "opaque-receipt" {
		t.Fatal(id, err)
	}
	_, err = api.SendText(t.Context(), "fixed-chat", "hello", "stable-outbox")
	if !errors.Is(err, ErrOutcomeUnknown) || sends.Load() != 2 || auth.Load() != 1 {
		t.Fatal(err, sends.Load(), auth.Load())
	}
}

func TestSendTextDefiniteRateLimitAndMalformedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body, retry string
		limited     bool
	}{
		{"rate_limit", 429, `{"code":99991400}`, "9", true},
		{"bad_rate_header", 429, `{}`, "bogus", true},
		{"legacy_rate_code", 400, `{"code":99991400}`, "", true},
		{"missing_id", 200, `{"code":0,"data":{}}`, "", false},
		{"malformed", 200, `{`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"fixture","expire":7200}`)
					return
				}
				sends.Add(1)
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			api, err := NewAPI(APIConfig{AppID: "app", AppSecret: "secret", BaseURL: server.URL, Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			_, err = api.SendText(t.Context(), "chat", "hello", "same-uuid")
			var limited *RateLimitedError
			if tc.limited {
				if !errors.As(err, &limited) || limited.After < time.Second {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatal(err)
			}
			if sends.Load() != 1 {
				t.Fatal("automatic retry", sends.Load())
			}
		})
	}
}

func TestAPIRejectsRedirectAndCancelledCredentialWait(t *testing.T) {
	var destination atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { destination.Add(1) }))
	defer other.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	api, err := NewAPI(APIConfig{AppID: "app", AppSecret: "secret", BaseURL: source.URL, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.SendText(t.Context(), "chat", "text", "uuid"); !errors.Is(err, ErrNotSent) || destination.Load() != 0 {
		t.Fatal("credential redirect", err)
	}
	api.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = api.tenantToken(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-api.gate
	for _, origin := range []string{"http://example.invalid", "https://user:secret@example.invalid", "https://example.invalid/path", "https://example.invalid?secret=x"} {
		if _, err = NewAPI(APIConfig{AppID: "app", AppSecret: "secret", BaseURL: origin, Now: time.Now}); err == nil {
			t.Fatal("accepted unsafe origin")
		}
	}
}

func TestTenantTokenTransientFailureDoesNotPermanentlyFailMessage(t *testing.T) {
	var auth, sends atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			if auth.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"fixture","expire":7200}`)
			return
		}
		sends.Add(1)
		var p map[string]string
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p["uuid"] != "same-original-uuid" {
			t.Error("send identity changed")
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"known-receipt"}}`)
	}))
	defer provider.Close()
	api, err := NewAPI(APIConfig{AppID: "app", AppSecret: "secret", BaseURL: provider.URL, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.SendText(t.Context(), "chat", "hello", "same-original-uuid"); !errors.Is(err, ErrBeforeSendTemporary) || sends.Load() != 0 {
		t.Fatal("transient token failure classified as send", err, sends.Load())
	}
	if id, err := api.SendText(t.Context(), "chat", "hello", "same-original-uuid"); err != nil || id != "known-receipt" || sends.Load() != 1 {
		t.Fatal(id, err, sends.Load())
	}
}
