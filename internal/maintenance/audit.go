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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/maintenance/status", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("maintenance: controller rejected status")
		}
		var s protocol.MaintenanceStatus
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil {
			return err
		}
		input := protocol.MaintenanceRequest{RequestID: opts.ID(), ExpectedRevision: s.Revision}
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/maintenance/audit", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("maintenance: audit dispatch outcome unknown; request %s: %w", input.RequestID, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("maintenance: audit dispatch not accepted")
		}
		return nil
	}
	c, err := Open(ctx, cfg, opts)
	if err != nil {
		return fmt.Errorf("maintenance: independent audit cannot acquire controller ownership: %w", err)
	}
	defer c.Close()
	if _, err := c.Command(ctx, "audit", protocol.MaintenanceRequest{RequestID: opts.ID(), ExpectedRevision: c.Status().Revision}); err != nil {
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
