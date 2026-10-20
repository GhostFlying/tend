package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Protocol constants shared with the server. They are repeated here because
// the runner must not import server packages.
const (
	rpcPath    = "/runner/v1/rpc"
	headerHost = "X-Tend-Host"
	headerRun  = "X-Tend-Run"
)

// credential is who a request acts as: the host, or one run.
type credential struct {
	runID string
	token string
}

// client sends envelopes to tend.
type client struct {
	base   string
	http   *http.Client
	hostID string
	token  string
	log    *slog.Logger
	now    func() time.Time
	// timeout, if set, bounds each request. Over tsnet a connection to a
	// server that went away may never fail on its own.
	timeout time.Duration

	mu sync.Mutex
	// downSince is when tend stopped answering; zero while it answers.
	downSince time.Time
}

// unreachable logs the first failure to reach tend; the callers log their
// own retries.
func (c *client) unreachable(ctx context.Context, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downSince.IsZero() {
		c.downSince = c.now()
		c.log.WarnContext(ctx, "tend is unreachable", "err", err)
	}
}

// reached logs that tend answers again after it was unreachable. Any
// answer counts, including a rejection.
func (c *client) reached(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.downSince.IsZero() {
		c.log.InfoContext(ctx, "reconnected to tend", "down_for", c.now().Sub(c.downSince).Round(time.Second).String())
		c.downSince = time.Time{}
	}
}

func (c *client) host() credential { return credential{token: c.token} }

func runCredential(as protocol.Assignment) credential {
	return credential{runID: as.RunID, token: as.RunToken}
}

// transportError is a failure in which the request may or may not have
// reached tend. The same envelope may be sent again.
type transportError struct{ err error }

func (e *transportError) Error() string { return "runner: transport: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// isRetryable reports whether the request may be sent again unchanged.
func isRetryable(err error) bool {
	_, ok := errors.AsType[*transportError](err)
	return ok
}

type rpcResponse struct {
	Operation *protocol.Operation `json:"operation"`
	Error     *protocol.Error     `json:"error"`
}

// do sends one envelope. Rejections by tend come back as *protocol.Error;
// anything that leaves the outcome open is a transportError.
func (c *client) do(ctx context.Context, cred credential, env *protocol.Envelope) (protocol.Operation, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return protocol.Operation{}, fmt.Errorf("runner: encode envelope: %w", err)
	}
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+rpcPath, bytes.NewReader(body))
	if err != nil {
		return protocol.Operation{}, fmt.Errorf("runner: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.token)
	if cred.runID != "" {
		req.Header.Set(headerRun, cred.runID)
	} else {
		req.Header.Set(headerHost, c.hostID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// A request that timed out found tend unreachable; one the caller
		// cancelled says nothing about tend.
		if !errors.Is(ctx.Err(), context.Canceled) {
			c.unreachable(ctx, err)
		}
		return protocol.Operation{}, &transportError{err}
	}
	defer resp.Body.Close()
	c.reached(ctx)
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return protocol.Operation{}, &transportError{fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)}
	}
	switch {
	case out.Error != nil && (out.Error.Code == protocol.CodeInternal || resp.StatusCode >= 500):
		return protocol.Operation{}, &transportError{out.Error}
	case out.Error != nil:
		return protocol.Operation{}, out.Error
	case out.Operation == nil:
		return protocol.Operation{}, &transportError{fmt.Errorf("empty response (status %d)", resp.StatusCode)}
	}
	return *out.Operation, nil
}
