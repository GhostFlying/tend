package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/netx/runnerdial"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

// The maintenance client owns no journal and never registers a runner. The
// supervisor uses the fixed service configuration and the same host principal.
type windowClient struct {
	host, endpoint, token string
	client                *http.Client
	ids                   *protocol.ULIDGen
}

func runnerWindowClient(cfg updater.Config) (*windowClient, error) {
	path := cfg.ServiceConfig
	if path == "" {
		for i, arg := range cfg.Args {
			if (arg == "--config" || arg == "-config") && i+1 < len(cfg.Args) {
				path = cfg.Args[i+1]
				break
			}
		}
	}
	if path == "" {
		return nil, errors.New("runner maintenance requires the service configuration")
	}
	if !filepath.IsAbs(path) && cfg.Dir != "" {
		path = filepath.Join(cfg.Dir, path)
	}
	var contents json.RawMessage
	if err := cfgfile.Load(path, &contents); err != nil {
		return nil, err
	}
	var network struct {
		Host    string                  `json:"host_id"`
		Server  string                  `json:"server_url"`
		Tailnet *json.RawMessage        `json:"tailnet"`
		Side    *runnerdial.SideChannel `json:"side_channel"`
	}
	if err := json.Unmarshal(contents, &network); err != nil {
		return nil, err
	}
	u, err := url.Parse(network.Server)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" || network.Host != cfg.InstanceID || network.Host == "" {
		return nil, errors.New("runner maintenance host or server configuration mismatch")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if network.Side != nil {
		client, err = network.Side.Client(network.Server)
		if err != nil {
			return nil, err
		}
	} else if network.Tailnet != nil {
		return nil, errors.New("embedded runner maintenance requires a configured independent side_channel")
	}
	token, err := updater.RunnerHostToken(cfg)
	if err != nil {
		return nil, err
	}
	client.Timeout = 4 * time.Second
	return &windowClient{host: network.Host, endpoint: strings.TrimRight(network.Server, "/") + "/runner/v1/rpc", token: token, client: client, ids: protocol.NewULIDGen(time.Now, nil)}, nil
}

func (c *windowClient) rpc(ctx context.Context, op string, input, out any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	env := protocol.Envelope{RequestID: c.ids.New(protocol.PrefixRequest), Target: protocol.Target{Type: "host", ID: c.host}, Operation: op, Payload: payload}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tend-Host", c.host)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var reply struct {
		Operation *protocol.Operation `json:"operation"`
		Error     *protocol.Error     `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return reply.Error
	}
	if resp.StatusCode != http.StatusOK || reply.Operation == nil || reply.Operation.RequestID != env.RequestID || reply.Operation.Status != protocol.OpSucceeded {
		return errors.New("runner maintenance RPC was not confirmed")
	}
	return json.Unmarshal(reply.Operation.Result, out)
}

func (c *windowClient) Target(ctx context.Context, process updater.Identity) (string, error) {
	var id string
	err := c.rpc(ctx, "runner.window_target", process, &id)
	// A historical server without this optional capability cannot have
	// admitted a forced window. Preserve its normal crash-restart contract.
	var wire *protocol.Error
	if errors.As(err, &wire) && wire.Code == protocol.CodeInvalidRequest && wire.Message == `operation "runner.window_target" is not part of the runner protocol` {
		return "", nil
	}
	return id, err
}
func (c *windowClient) Retire(ctx context.Context, req protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	var result protocol.RunnerRetirementResult
	err := c.rpc(ctx, "runner.rolling_retire", req, &result)
	if err == nil && (result.UpgradeID != req.UpgradeID || result.Process != req.Process || result.Allowed && result.AuthorizationID == "" || req.AuthorizationID != "" && result.AuthorizationID != req.AuthorizationID) {
		err = errors.New("retirement result does not match the original process and grant")
	}
	return result, err
}
