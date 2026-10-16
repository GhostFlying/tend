package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// client calls the tend server's owner API.
type client struct {
	http   *http.Client
	server string
	token  string
}

// transportError is a failure to get an answer from the server at all, or
// an answer from something in front of it. The request may or may not
// have reached tend, so it is safe to resend only under the same request
// ID.
type transportError struct{ err error }

func (t transportError) Error() string { return "tend is unreachable: " + t.err.Error() }
func (t transportError) Unwrap() error { return t.err }

func isTransport(err error) bool {
	var t transportError
	return errors.As(err, &t)
}

// do sends a request and decodes the answer into out. A protocol error
// the server returns is a *protocol.Error.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.server, "/")+path, r)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError{err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return transportError{err}
	}
	if resp.StatusCode != http.StatusOK {
		var res protocol.APIResponse
		if json.Unmarshal(b, &res) == nil && res.Error != nil {
			return res.Error
		}
		return transportError{fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s: decode the answer: %w", method, path, err)
	}
	return nil
}
