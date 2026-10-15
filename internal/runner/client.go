package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
		return protocol.Operation{}, &transportError{err}
	}
	defer resp.Body.Close()
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
