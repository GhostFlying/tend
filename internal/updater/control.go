package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type ControlRequest struct {
	ExpectedPhase string `json:"expected_phase,omitempty"`
	ID            string `json:"id,omitempty"`
	Release       string `json:"release,omitempty"`
	PID           int    `json:"pid,omitempty"`
}
type ControlResponse struct {
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

func SocketPath(cfg Config) string { return filepath.Join(cfg.StateDir, "control.sock") }

// Serve owns only its socket and polling loop. Stopping the updater leaves the
// managed child running; another supervisor reconciles its exact identity.
func (s *Supervisor) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	path := SocketPath(s.cfg)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("updater: control path exists and is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(path)
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(ControlResponse{State: s.Status()})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req ControlRequest
		d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(ControlResponse{Error: "invalid updater request"})
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var err error
		switch r.URL.Path {
		case "/bootstrap":
			err = s.Bootstrap(r.Context(), req.PID)
		case "/upgrade":
			err = s.Upgrade(r.Context(), req.ID, req.Release)
		case "/restart":
			err = s.Restart(req.ID)
		case "/retry":
			err = s.Retry(req.ID)
		case "/cancel":
			err = s.CancelExpected(req.ID, req.ExpectedPhase)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		response := ControlResponse{State: s.Status()}
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			response.Error = err.Error()
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.Step(ctx)
			}
		}
	}()
	defer func() { cancel(); _ = server.Close(); <-stopped }()
	go func() { <-ctx.Done(); _ = server.Close() }()
	err = server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}
func Call(ctx context.Context, cfg Config, operation string, request ControlRequest) (State, error) {
	return CallSocket(ctx, SocketPath(cfg), operation, request)
}

// CallSocket controls a fixed configured supervisor socket without importing its configuration.
func CallSocket(ctx context.Context, socket string, operation string, request ControlRequest) (State, error) {
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	raw, err := json.Marshal(request)
	if err != nil {
		return State{}, err
	}
	method := http.MethodPost
	if operation == "status" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/"+operation, bytes.NewReader(raw))
	if err != nil {
		return State{}, err
	}
	client := &http.Client{Transport: tr, Timeout: 45 * time.Second}
	r, err := client.Do(req)
	if err != nil {
		return State{}, errors.New("updater: supervisor unavailable")
	}
	defer r.Body.Close()
	var response ControlResponse
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&response); err != nil {
		return State{}, errors.New("updater: invalid supervisor response")
	}
	if r.StatusCode != http.StatusOK || response.Error != "" {
		return response.State, errors.New(response.Error)
	}
	return response.State, nil
}
