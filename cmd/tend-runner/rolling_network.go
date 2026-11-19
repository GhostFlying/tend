package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"

	"tailscale.com/client/local"
)

type sideChannelConfig struct {
	Kind   string `json:"kind"`
	Socket string `json:"socket,omitempty"`
}

// Each generation dials through the OS daemon without opening the predecessor's
// tsnet state. Keep the HTTP authority, TLS name and credentials of server_url.
func (c sideChannelConfig) client(server string, daemon *local.Client) (*http.Client, error) {
	if c.Kind != "tailscaled" {
		return nil, errors.New("unsupported runner side_channel kind")
	}
	if c.Socket != "" && !filepath.IsAbs(c.Socket) {
		return nil, errors.New("side_channel socket must be absolute")
	}
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid server_url for runner side_channel")
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return nil, errors.New("invalid server port for runner side_channel")
	}
	addr := net.JoinHostPort(u.Hostname(), port)
	tr := &http.Transport{ForceAttemptHTTP2: true, DialContext: func(ctx context.Context, _, target string) (net.Conn, error) {
		if target != addr {
			return nil, errors.New("side_channel is bound to the configured server address")
		}
		return daemon.DialTCP(ctx, u.Hostname(), uint16(number))
	}}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
