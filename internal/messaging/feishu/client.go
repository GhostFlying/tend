package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrBeforeSendTemporary = errors.New("feishu: credentials temporarily unavailable before send")
	ErrNotSent             = errors.New("feishu: message was not sent")
	ErrOutcomeUnknown      = errors.New("feishu: send outcome is unknown")
)

// RateLimitedError is a definite rejection; retry keeps the original UUID.
type RateLimitedError struct{ After time.Duration }

func (*RateLimitedError) Error() string { return "feishu: rate limited" }

type APIConfig struct {
	AppID, AppSecret, BaseURL string
	HTTP                      *http.Client
	Now                       func() time.Time
}

// API holds tenant credentials in memory. It never retries a message send.
type API struct {
	config     APIConfig
	gate       chan struct{}
	token      string
	tokenUntil time.Time
}

func NewAPI(config APIConfig) (*API, error) {
	if config.AppID == "" || config.AppSecret == "" || config.Now == nil {
		return nil, errors.New("feishu: API configuration is incomplete")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://open.feishu.cn"
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("feishu: invalid API origin")
	}
	if config.HTTP == nil {
		config.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *config.HTTP
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTP = &copyClient
	return &API{config: config, gate: make(chan struct{}, 1)}, nil
}

func (a *API) tenantToken(ctx context.Context) (string, error) {
	select {
	case a.gate <- struct{}{}:
		defer func() { <-a.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if a.token != "" && a.config.Now().Before(a.tokenUntil) {
		return a.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": a.config.AppID, "app_secret": a.config.AppSecret})
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.BaseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", ErrNotSent
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := a.config.HTTP.Do(r)
	if err != nil {
		return "", ErrBeforeSendTemporary
	}
	defer response.Body.Close()
	var result struct {
		Code   int    `json:"code"`
		Token  string `json:"tenant_access_token"`
		Expire int64  `json:"expire"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCallbackBytes+1))
	if response.StatusCode == 429 {
		return "", &RateLimitedError{After: rateDelay(response.Header)}
	}
	if response.StatusCode >= 500 || err != nil || len(data) > maxCallbackBytes {
		return "", ErrBeforeSendTemporary
	}
	if response.StatusCode != 200 {
		return "", ErrNotSent
	}
	if json.Unmarshal(data, &result) != nil {
		return "", ErrBeforeSendTemporary
	}
	if result.Code == 99991400 {
		return "", &RateLimitedError{After: rateDelay(response.Header)}
	}
	if result.Code != 0 {
		return "", ErrNotSent
	}
	if result.Token == "" || result.Expire <= 60 || result.Expire > 86400 {
		return "", ErrBeforeSendTemporary
	}

	a.token = result.Token
	a.tokenUntil = a.config.Now().Add(time.Duration(result.Expire-60) * time.Second)
	return a.token, nil
}

// SendText sends a frozen chat destination and stable delivery UUID. Loss of its
// receipt is Unknown even when the server might have accepted the same UUID.
func (a *API) SendText(ctx context.Context, chat, text, uuid string) (string, error) {
	if strings.TrimSpace(chat) == "" || strings.TrimSpace(text) == "" || len(text) > 4000 || uuid == "" || len(uuid) > 50 {
		return "", ErrNotSent
	}
	token, err := a.tenantToken(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", ErrBeforeSendTemporary
		}
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	body, _ := json.Marshal(map[string]string{"receive_id": chat, "msg_type": "text", "content": string(content), "uuid": uuid})
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.BaseURL+"/open-apis/im/v1/messages?receive_id_type=chat_id", bytes.NewReader(body))
	if err != nil {
		return "", ErrNotSent
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := a.config.HTTP.Do(r)
	if err != nil {
		return "", ErrOutcomeUnknown
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCallbackBytes+1))
	if err != nil || len(data) > maxCallbackBytes {
		return "", ErrOutcomeUnknown
	}
	if response.StatusCode == http.StatusTooManyRequests {
		value := response.Header.Get("x-ogw-ratelimit-reset")
		if value == "" {
			value = response.Header.Get("Retry-After")
		}
		seconds, e := strconv.Atoi(value)
		if e != nil {
			seconds = 1
		}
		seconds = max(1, min(seconds, 3600))
		return "", &RateLimitedError{After: time.Duration(seconds) * time.Second}
	}
	if response.StatusCode >= 500 {
		return "", ErrOutcomeUnknown
	}
	var result struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil {
		return "", ErrOutcomeUnknown
	}
	if result.Code == 99991400 {
		value := response.Header.Get("x-ogw-ratelimit-reset")
		seconds, e := strconv.Atoi(value)
		if e != nil {
			seconds = 1
		}
		return "", &RateLimitedError{After: time.Duration(max(1, min(seconds, 3600))) * time.Second}
	}
	if result.Code != 0 {
		return "", ErrNotSent
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", ErrOutcomeUnknown
	}
	if result.Data.MessageID == "" {
		return "", ErrOutcomeUnknown
	}
	return result.Data.MessageID, nil
}

func rateDelay(header http.Header) time.Duration {
	value := header.Get("x-ogw-ratelimit-reset")
	if value == "" {
		value = header.Get("Retry-After")
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		seconds = 1
	}
	return time.Duration(max(1, min(seconds, 3600))) * time.Second
}
