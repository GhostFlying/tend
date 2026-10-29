package upgradectl

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

// ArchiveControl uses the current local listener and app authority, without
// requiring a release or installer. Hosts remain explicit, independent CASs.
type ArchiveControl struct {
	App           *app.App
	ObserveHosted func(context.Context) (bool, error)
}

func (c ArchiveControl) Status(ctx context.Context) (protocol.ArchiveOnlineStatus, error) {
	v, err := c.App.ArchiveOnlineStatus(ctx)
	if err != nil {
		return v, err
	}
	v.HostedDrained = true
	if c.ObserveHosted != nil {
		v.HostedDrained, err = c.ObserveHosted(ctx)
		if err != nil {
			return v, err
		}
	}
	if !v.Preparation.Prepared {
		v.Blockers = append(v.Blockers, "archive preparation is not active")
	}
	if v.ActiveOwnerRequests != 0 {
		v.Blockers = append(v.Blockers, "admitted owner requests remain")
	}
	if !v.HostedDrained {
		v.Blockers = append(v.Blockers, "hosted operations are active or uncertain")
	}
	v.OnlineReady = len(v.Blockers) == 0
	for _, h := range v.Hosts {
		if len(h.Blockers) > 0 {
			v.OnlineReady = false
		}
	}
	return v, nil
}
func (c ArchiveControl) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		var err error
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/archive/status":
			result, err = c.Status(r.Context())
		case r.Method == http.MethodPost && (r.URL.Path == "/archive/prepare" || r.URL.Path == "/archive/cancel"):
			var input protocol.ArchiveControlRequest
			if err = decode(r, &input); err == nil {
				result, err = c.App.ChangeArchivePreparation(r.Context(), input, r.URL.Path == "/archive/prepare")
			}
		case r.Method == http.MethodPost && r.URL.Path == "/archive/host":
			var input protocol.ArchiveHostControlRequest
			if err = decode(r, &input); err == nil {
				payload, _ := json.Marshal(map[string]string{"mode": input.Mode})
				env := &protocol.Envelope{RequestID: input.RequestID, Operation: "hosts.maintenance", Target: protocol.Target{Type: "host", ID: input.HostID}, ExpectedControlRevision: &input.ExpectedControlRevision, Payload: payload, Reason: "Apply explicit host maintenance for an offline archive cutover or its cancellation."}
				var op protocol.Operation
				op, err = c.App.Execute(r.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, app.HostMaintenance{})
				if err == nil {
					if op.Error != nil {
						err = op.Error
					} else {
						result = json.RawMessage(op.Result)
					}
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			code := protocol.CodeOf(err)
			if code == "" {
				code = protocol.CodeInternal
			}
			_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: protocol.Errorf(code, "archive operation rejected; inspect status and retry the original request")})
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
