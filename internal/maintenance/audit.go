package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Audit submits one durable inspection. When the controller is stopped it can
// dispatch under the same exclusive state lock, then leave the native work alive.
func Audit(ctx context.Context, cfg Config, opts Options) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.ControlSocket)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	status, err := readAuditStatus(ctx, client)
	if err == nil {
		return submitScheduledAudit(ctx, client, status, opts.ID())
	}
	c, err := Open(ctx, cfg, opts)
	if err != nil {
		return fmt.Errorf("maintenance: independent audit cannot acquire controller ownership: %w", err)
	}
	defer c.Close()
	if _, err := c.Command(ctx, "audit", protocol.MaintenanceRequest{ScheduledAudit: true, RequestID: opts.ID(), ExpectedRevision: c.Status().Revision}); err != nil {
		return err
	}
	for {
		before := c.Status()
		if err := c.Step(ctx); err != nil {
			return err
		}
		after := c.Status()
		if after.Revision == before.Revision || after.Active != nil && after.Active.InputID != "" {
			return nil
		}
	}
}

func readAuditStatus(ctx context.Context, client *http.Client) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/maintenance/status", nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func submitScheduledAudit(ctx context.Context, client *http.Client, status *http.Response, requestID string) error {
	for attempt := range 3 {
		var state protocol.MaintenanceStatus
		if status.StatusCode != http.StatusOK {
			status.Body.Close()
			return errors.New("maintenance: controller rejected status")
		}
		err := json.NewDecoder(io.LimitReader(status.Body, 1<<20)).Decode(&state)
		status.Body.Close()
		if err != nil {
			return err
		}
		input := protocol.MaintenanceRequest{ScheduledAudit: true, RequestID: requestID, ExpectedRevision: state.Revision}
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/maintenance/audit", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("maintenance: audit dispatch outcome unknown; request %s: %w", input.RequestID, err)
		}
		if resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}
		var rejection struct {
			Code protocol.Code `json:"code"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rejection)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict || err != nil || rejection.Code != protocol.CodeStaleRevision || attempt == 2 {
			return fmt.Errorf("maintenance: audit dispatch not accepted; request %s, revision %d, HTTP %d", input.RequestID, input.ExpectedRevision, resp.StatusCode)
		}
		// The controller explicitly rejected this CAS before committing the
		// receipt. Refresh while retaining the same timer request identity.
		status, err = readAuditStatus(ctx, client)
		if err != nil {
			return err
		}
	}
	return errors.New("maintenance: audit dispatch did not settle")
}
