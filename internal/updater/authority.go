package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HTTPAuthority uses a fixed local service endpoint. Unix sockets let a runner
// relay server-issued grants without lending its tsnet identity to the updater.
// The candidate must publish server-confirmed health on that same socket.
type HTTPAuthority struct {
	cfg    Config
	client *http.Client
}

func NewHTTPAuthority(cfg Config) (*HTTPAuthority, error) {
	for _, raw := range []string{cfg.AuthorizeURL, cfg.HealthURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, errors.New("updater: invalid fixed authority endpoint")
		}
		if cfg.AuthoritySocket == "" && u.Scheme == "http" && u.Hostname() != "localhost" && net.ParseIP(u.Hostname()) == nil {
			return nil, errors.New("updater: cleartext authority must use loopback")
		}
		if cfg.AuthoritySocket == "" && u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
			return nil, errors.New("updater: cleartext authority must use loopback")
		}
	}
	tr := &http.Transport{Proxy: nil}
	if cfg.AuthoritySocket != "" {
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", cfg.AuthoritySocket)
		}
	}
	return &HTTPAuthority{cfg: cfg, client: &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("updater: authority redirects are forbidden")
	}}}, nil
}
func (a *HTTPAuthority) request(ctx context.Context, path string, b Binding, out any) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.AuthorityTokenFile != "" {
		token, err := os.ReadFile(a.cfg.AuthorityTokenFile)
		if err != nil {
			return errors.New("updater: authority credential unavailable")
		}
		v := strings.TrimSpace(string(token))
		if v == "" || strings.ContainsAny(v, "\r\n") {
			return errors.New("updater: invalid authority credential")
		}
		req.Header.Set("Authorization", "Bearer "+v)
	}
	response, err := a.client.Do(req)
	if err != nil {
		return errors.New("updater: authority transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("updater: authority returned status %d", response.StatusCode)
	}
	d := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("updater: invalid authority response")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("updater: trailing authority response")
	}
	return nil
}
func (a *HTTPAuthority) Authorize(ctx context.Context, b Binding) (Grant, error) {
	var g Grant
	err := a.request(ctx, a.cfg.AuthorizeURL, b, &g)
	return g, err
}
func (a *HTTPAuthority) Health(ctx context.Context, b Binding) (Health, error) {
	var h Health
	err := a.request(ctx, a.cfg.HealthURL, b, &h)
	return h, err
}
