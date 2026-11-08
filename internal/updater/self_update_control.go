package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"io"
	"net"
	"net/http"
	"time"
)

func (s *Supervisor) selfHandler(w http.ResponseWriter, r *http.Request) {
	var status SelfUpdateStatus
	var err error
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/self-update/status":
		status, err = s.selfStatus(r.Context())
	case r.Method == http.MethodPost && r.URL.Path == "/self-update/apply":
		var request SelfUpdateRequest
		d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		d.DisallowUnknownFields()
		if e := d.Decode(&request); e != nil {
			err = e
		} else if e = d.Decode(new(any)); e != io.EOF {
			err = errors.New("updater: trailing self-update request")
		} else {
			status, err = s.requestSelf(r.Context(), request)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		status.Error = err.Error()
		w.WriteHeader(http.StatusConflict)
	}
	_ = json.NewEncoder(w).Encode(status)
}

// SelfUpdateCall uses a distinct endpoint. The legacy status and service
// installation receipt remain unchanged for old local control clients.
func SelfUpdateCall(ctx context.Context, socket string, request *SelfUpdateRequest) (SelfUpdateStatus, error) {
	var out SelfUpdateStatus
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	method, path := http.MethodGet, "status"
	var body []byte
	if request != nil {
		method, path = http.MethodPost, "apply"
		var err error
		body, err = json.Marshal(request)
		if err != nil {
			return out, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/self-update/"+path, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	response, err := (&http.Client{Transport: tr, Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return out, errors.New("updater: self-update observation unavailable; reconcile original request")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return out, errors.New("updater: supervisor-first capability unavailable; standalone bootstrap required")
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&out); err != nil {
		return out, err
	}
	if response.StatusCode != http.StatusOK || out.Error != "" {
		return out, errors.New(out.Error)
	}
	if !out.Supported {
		return out, errors.New("updater: supervisor-first bootstrap required")
	}
	return out, nil
}

// SelfUpdateSupported uses the legacy read-only status route. Old supervisors
// need not parse any new request fields or recognize a new control operation.
func SelfUpdateSupported(ctx context.Context, socket string) (bool, error) {
	_, supported, err := ObserveStatus(ctx, socket)
	return supported, err
}

// ObserveStatus adds a response-header capability observation to the unchanged
// legacy body, allowing the runner to reuse its existing receipt read.
func ObserveStatus(ctx context.Context, socket string) (State, bool, error) {
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/status", nil)
	if err != nil {
		return State{}, false, err
	}
	response, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return State{}, false, err
	}
	defer response.Body.Close()
	var decoded ControlResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&decoded); err != nil {
		return State{}, false, err
	}
	if response.StatusCode != http.StatusOK || decoded.Error != "" {
		return decoded.State, false, errors.New("updater: status unavailable")
	}
	return decoded.State, response.Header.Get("Tend-Updater-Capabilities") == protocol.CapabilityUpdaterSelfUpdate, nil
}
