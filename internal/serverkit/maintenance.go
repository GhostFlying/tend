package serverkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func maintenanceReader(socket string) func(context.Context) (protocol.MaintenanceOverview, error) {
	return func(ctx context.Context) (protocol.MaintenanceOverview, error) {
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		defer tr.CloseIdleConnections()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/maintenance/status", nil)
		if err != nil {
			return protocol.MaintenanceOverview{}, err
		}
		resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Do(req)
		if err != nil {
			return protocol.MaintenanceOverview{}, errors.New("maintenance reporting unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return protocol.MaintenanceOverview{}, errors.New("maintenance reporting refused")
		}
		var s protocol.MaintenanceStatus
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil {
			return protocol.MaintenanceOverview{}, err
		}
		v := protocol.MaintenanceOverview{Configured: true, Available: true, LastCheck: s.LastCheck, LastAudit: s.LastAudit, Paused: s.Paused, OpenCases: s.OpenCases, Pending: len(s.Pending), Agent: s.Backend.Agent, Error: s.LastError}
		if s.NativeError != "" {
			v.Error = s.NativeError
		}
		if s.Active != nil {
			v.Agent = s.Active.Backend.Agent
			v.SessionID = s.Active.SessionID
			v.Phase = s.Active.Phase
		}
		if s.Upgrade != nil {
			v.UpgradePhase = s.Upgrade.Phase
		}
		return v, nil
	}
}
