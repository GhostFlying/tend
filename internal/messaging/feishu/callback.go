// Package feishu authenticates Feishu callbacks before crossing the app boundary.
package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxCallbackBytes = 1 << 20

// Incoming preserves provider identity; the application resolves owner authority.
type Incoming struct {
	EventID, TenantKey, AppID             string
	MessageID, ChatID, SenderID, ParentID string
	Text                                  string
}

// Receiver durably accepts or rejects a single provider event. Successful return
// means an event may be acknowledged; it does not mean a model has processed it.
type Receiver interface {
	Receive(context.Context, Incoming) error
}

type CallbackConfig struct {
	AppID, TenantKey, VerificationToken, EncryptKey string
	Now                                             func() time.Time
}

// Callback handles only authenticated text events. Unsupported message types are
// explicitly rejected so attachments cannot silently become empty owner messages.
type Callback struct {
	config   CallbackConfig
	receiver Receiver
}

func NewCallback(config CallbackConfig, receiver Receiver) (*Callback, error) {
	if config.AppID == "" || config.TenantKey == "" || config.VerificationToken == "" || config.EncryptKey == "" || config.Now == nil || receiver == nil {
		return nil, errors.New("feishu: callback configuration is incomplete")
	}
	return &Callback{config: config, receiver: receiver}, nil
}

func (c *Callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCallbackBytes+1))
	if err != nil || len(body) > maxCallbackBytes {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	signedBody := body
	var encrypted struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &encrypted) != nil {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	if encrypted.Encrypt != "" {
		body, err = decrypt(encrypted.Encrypt, c.config.EncryptKey)
		if err != nil {
			http.Error(w, "invalid encrypted callback", http.StatusBadRequest)
			return
		}
	}
	var event struct {
		Type      string `json:"type"`
		Token     string `json:"token"`
		Challenge string `json:"challenge"`
		Schema    string `json:"schema"`
		Header    struct {
			EventID   string `json:"event_id"`
			EventType string `json:"event_type"`
			Token     string `json:"token"`
			AppID     string `json:"app_id"`
			TenantKey string `json:"tenant_key"`
		} `json:"header"`
		Event struct {
			Sender struct {
				SenderType string `json:"sender_type"`
				SenderID   struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
			Message struct {
				MessageID   string `json:"message_id"`
				ChatID      string `json:"chat_id"`
				ChatType    string `json:"chat_type"`
				ParentID    string `json:"parent_id"`
				MessageType string `json:"message_type"`
				Content     string `json:"content"`
			} `json:"message"`
		} `json:"event"`
	}
	if json.Unmarshal(body, &event) != nil {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if event.Type == "url_verification" {
		if !equalSecret(event.Token, c.config.VerificationToken) || event.Challenge == "" || len(event.Challenge) > 4096 {
			http.Error(w, "callback authentication failed", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": event.Challenge})
		return
	}
	// Initial URL verification uses the verification token (and decryption for
	// encrypted probes), not ordinary event signature headers.
	if !c.validSignature(r.Header, signedBody) {
		http.Error(w, "callback authentication failed", http.StatusUnauthorized)
		return
	}
	h := event.Header
	if event.Schema != "2.0" || !equalSecret(h.Token, c.config.VerificationToken) || h.AppID != c.config.AppID || h.TenantKey != c.config.TenantKey {
		http.Error(w, "callback authentication failed", http.StatusUnauthorized)
		return
	}
	if h.EventType != "im.message.receive_v1" {
		_, _ = io.WriteString(w, `{}`)
		return
	}
	m := event.Event.Message
	var content struct {
		Text string `json:"text"`
	}
	if event.Event.Sender.SenderType != "user" || m.MessageType != "text" || m.ChatType != "p2p" || json.Unmarshal([]byte(m.Content), &content) != nil || strings.TrimSpace(content.Text) == "" || h.EventID == "" || m.MessageID == "" || m.ChatID == "" || event.Event.Sender.SenderID.OpenID == "" {
		http.Error(w, "unsupported message", http.StatusUnprocessableEntity)
		return
	}
	input := Incoming{EventID: h.EventID, TenantKey: h.TenantKey, AppID: h.AppID, MessageID: m.MessageID, ChatID: m.ChatID, SenderID: event.Event.Sender.SenderID.OpenID, ParentID: m.ParentID, Text: content.Text}
	if err = c.receiver.Receive(r.Context(), input); err != nil {
		http.Error(w, "message was not accepted", http.StatusServiceUnavailable)
		return
	}
	_, _ = io.WriteString(w, `{}`)
}

func equalSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (c *Callback) validSignature(header http.Header, body []byte) bool {
	timestamp, nonce := header.Get("X-Lark-Request-Timestamp"), header.Get("X-Lark-Request-Nonce")
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || nonce == "" || len(nonce) > 256 {
		return false
	}
	// Compare bounded seconds rather than subtracting arbitrary remote values.
	now := c.config.Now().Unix()
	if stamp < now-300 || stamp > now+300 {
		return false
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, timestamp+nonce+c.config.EncryptKey)
	_, _ = hash.Write(body)
	actual, err := hex.DecodeString(header.Get("X-Lark-Signature"))
	return err == nil && subtle.ConstantTimeCompare(actual, hash.Sum(nil)) == 1
}

func decrypt(encrypted, key string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(raw) < 2*aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		return nil, errors.New("invalid cipher text")
	}
	digest := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(raw)-aes.BlockSize)
	cipher.NewCBCDecrypter(block, raw[:aes.BlockSize]).CryptBlocks(plain, raw[aes.BlockSize:])
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || !bytes.Equal(plain[len(plain)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errors.New("invalid padding")
	}
	return plain[:len(plain)-padding], nil
}
