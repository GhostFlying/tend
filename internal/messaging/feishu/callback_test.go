package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type receiptSink struct {
	calls int
	input Incoming
	err   error
}

func (s *receiptSink) Receive(_ context.Context, input Incoming) error {
	s.calls++
	s.input = input
	return s.err
}

func callbackFixture(t *testing.T) (*Callback, *receiptSink) {
	t.Helper()
	sink := &receiptSink{}
	c, err := NewCallback(CallbackConfig{AppID: "app-fixture", TenantKey: "tenant-fixture", VerificationToken: "verify-fixture", EncryptKey: "encryption-fixture", Now: func() time.Time { return time.Unix(2000000000, 0) }}, sink)
	if err != nil {
		t.Fatal(err)
	}
	return c, sink
}

func messageFixture() []byte {
	return []byte(`{"schema":"2.0","header":{"event_id":"ev-1","event_type":"im.message.receive_v1","token":"verify-fixture","app_id":"app-fixture","tenant_key":"tenant-fixture"},"event":{"sender":{"sender_type":"user","sender_id":{"open_id":"owner-fixture"}},"message":{"message_id":"om-1","chat_id":"oc-1","chat_type":"p2p","message_type":"text","parent_id":"om-parent","content":"{\"text\":\"hello\"}"}}}`)
}

func signedRequest(c *Callback, body []byte, seconds int64) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/hooks/feishu", bytes.NewReader(body))
	ts := strconv.FormatInt(seconds, 10)
	r.Header.Set("X-Lark-Request-Timestamp", ts)
	r.Header.Set("X-Lark-Request-Nonce", "nonce-fixture")
	h := sha256.New()
	_, _ = h.Write([]byte(ts + "nonce-fixture" + c.config.EncryptKey))
	_, _ = h.Write(body)
	r.Header.Set("X-Lark-Signature", hex.EncodeToString(h.Sum(nil)))
	return r
}

func encryptedFixture(t *testing.T, plain []byte, key string) []byte {
	t.Helper()
	digest := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := bytes.Repeat([]byte{0x23}, aes.BlockSize)
	raw := append([]byte{}, iv...)
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plain)
	raw = append(raw, ciphertext...)
	body, err := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCallbackAuthenticatesBeforeDurableReceipt(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(strconv.FormatBool(encrypted), func(t *testing.T) {
			c, sink := callbackFixture(t)
			body := messageFixture()
			if encrypted {
				body = encryptedFixture(t, body, c.config.EncryptKey)
			}
			w := httptest.NewRecorder()
			c.ServeHTTP(w, signedRequest(c, body, c.config.Now().Unix()))
			if w.Code != http.StatusOK || sink.calls != 1 || sink.input.EventID != "ev-1" || sink.input.SenderID != "owner-fixture" || sink.input.ParentID != "om-parent" || sink.input.Text != "hello" {
				t.Fatalf("code=%d calls=%d input=%+v", w.Code, sink.calls, sink.input)
			}
		})
	}
}

func TestCallbackRejectsUntrustedInputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func([]byte) []byte
		signature string
		delta     int64
	}{
		{name: "signature", signature: "invalid"},
		{name: "old", delta: -301},
		{name: "future", delta: 301},
		{name: "token", change: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("verify-fixture"), []byte("other-token")) }},
		{name: "tenant", change: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("tenant-fixture"), []byte("other-tenant")) }},
		{name: "app", change: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("app-fixture"), []byte("other-app")) }},
		{name: "bot", change: func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte(`"sender_type":"user"`), []byte(`"sender_type":"app"`))
		}},
		{name: "group", change: func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte(`"chat_type":"p2p"`), []byte(`"chat_type":"group"`))
		}},
		{name: "unsupported_attachment", change: func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte(`"message_type":"text"`), []byte(`"message_type":"image"`))
		}},
		{name: "bad_cipher", change: func([]byte) []byte { return []byte(`{"encrypt":"AAAA"}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, sink := callbackFixture(t)
			body := messageFixture()
			if tc.change != nil {
				body = tc.change(body)
			}
			r := signedRequest(c, body, c.config.Now().Unix()+tc.delta)
			if tc.signature != "" {
				r.Header.Set("X-Lark-Signature", tc.signature)
			}
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code < 400 || sink.calls != 0 {
				t.Fatalf("code=%d calls=%d", w.Code, sink.calls)
			}
		})
	}
}

func TestCallbackDoesNotAcknowledgeUnknownAcceptance(t *testing.T) {
	c, sink := callbackFixture(t)
	sink.err = errors.New("private database error must not escape")
	w := httptest.NewRecorder()
	c.ServeHTTP(w, signedRequest(c, messageFixture(), c.config.Now().Unix()))
	if w.Code != http.StatusServiceUnavailable || bytes.Contains(w.Body.Bytes(), []byte("private")) {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestChallengeRequiresSameAuthentication(t *testing.T) {
	c, sink := callbackFixture(t)
	body := encryptedFixture(t, []byte(`{"type":"url_verification","token":"verify-fixture","challenge":"challenge-fixture"}`), c.config.EncryptKey)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, signedRequest(c, body, c.config.Now().Unix()))
	var result map[string]string
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != 200 || result["challenge"] != "challenge-fixture" || sink.calls != 0 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}
