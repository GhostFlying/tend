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
	"os"
	"path/filepath"
	"time"
)

type ControlRequest struct {
	BoundedWindow              *protocol.BoundedMaintenanceState           `json:"bounded_window,omitempty"`
	BoundedAuthorization       *protocol.BoundedMaintenanceAuthorization   `json:"bounded_authorization,omitempty"`
	BoundedRunnerGrant         *protocol.BoundedRunnerMaintenanceGrant     `json:"bounded_runner_grant,omitempty"`
	BoundedAction              string                                      `json:"bounded_action,omitempty"`
	MaintenanceReceiptRecovery *protocol.MaintenanceReceiptRecoveryRequest `json:"maintenance_receipt_recovery,omitempty"`
	MaintenanceContinuation    *protocol.MaintenanceContinuation           `json:"maintenance_continuation,omitempty"`
	Maintenance                *protocol.CommittedMaintenancePlan          `json:"maintenance,omitempty"`
	MaintenanceReceipt         *protocol.CommittedMaintenanceReceipt       `json:"maintenance_receipt,omitempty"`
	Recovery                   *protocol.ForwardRecoveryPlan               `json:"recovery,omitempty"`
	Receipt                    *protocol.RecoveryReceipt                   `json:"receipt,omitempty"`
	ExpectedGeneration         uint64                                      `json:"expected_generation,omitempty"`

	Configuration  *protocol.RunnerConfiguration `json:"configuration,omitempty"`
	Candidate      string                        `json:"candidate,omitempty"`
	ManifestSHA256 string                        `json:"manifest_sha256,omitempty"`
	ExpectedPhase  string                        `json:"expected_phase,omitempty"`
	ID             string                        `json:"id,omitempty"`
	Release        string                        `json:"release,omitempty"`
	PID            int                           `json:"pid,omitempty"`
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
		case "/bounded/cancel":
			if req.Maintenance == nil || req.BoundedAuthorization == nil {
				err = errors.New("updater: bounded cancellation inputs required")
			} else {
				err = s.CancelBoundedMaintenance(r.Context(), *req.Maintenance, *req.BoundedAuthorization)
			}
		case "/bounded/authorize":
			if req.BoundedAuthorization == nil {
				err = errors.New("updater: independent bounded authorization required")
			} else if req.BoundedAuthorization.Role == "server" && req.Maintenance != nil {
				err = s.authorizeBoundedMaintenanceWindow(r.Context(), *req.Maintenance, *req.BoundedAuthorization, req.BoundedAction, req.BoundedWindow)
			} else if req.BoundedAuthorization.Role == "runner" && req.BoundedRunnerGrant != nil {
				err = s.AuthorizeBoundedRunnerMaintenance(r.Context(), *req.BoundedRunnerGrant, req.BoundedAction)
			} else {
				err = errors.New("updater: bounded source plan required")
			}
		case "/bootstrap":
			err = s.Bootstrap(r.Context(), req.PID)
		case "/upgrade":
			err = s.UpgradeConfigured(r.Context(), req.ID, req.Release, req.Configuration)
		case "/stage-config":
			err = s.StageConfiguration(r.Context(), req.ID, req.ManifestSHA256, req.Candidate, req.Configuration)
		case "/restart":
			err = s.Restart(req.ID)
		case "/maintenance/prepare", "/maintenance/apply", "/maintenance/cancel", "/maintenance/finish":
			if req.Maintenance == nil {
				err = errors.New("updater: maintenance plan required")
			} else {
				switch r.URL.Path {
				case "/maintenance/prepare":
					err = s.PrepareCommittedMaintenance(r.Context(), *req.Maintenance, req.ExpectedGeneration)
				case "/maintenance/apply":
					err = s.AuthorizeCommittedMaintenance(r.Context(), *req.Maintenance, req.ExpectedGeneration)
				case "/maintenance/finish":
					err = s.FinishCommittedMaintenance(r.Context(), *req.Maintenance, req.ExpectedGeneration)
				case "/maintenance/cancel":
					err = s.CancelCommittedMaintenance(r.Context(), *req.Maintenance, req.ExpectedGeneration)
				}
			}
		case "/maintenance/recover-stop":
			if req.Maintenance == nil || req.MaintenanceReceiptRecovery == nil || req.ExpectedGeneration != req.MaintenanceReceiptRecovery.ExpectedGeneration {
				err = errors.New("updater: exact receipt recovery request and generation required")
			} else {
				err = s.RecoverCommittedMaintenance(r.Context(), *req.Maintenance, *req.MaintenanceReceiptRecovery)
			}
		case "/maintenance/resume":
			if req.Maintenance == nil || req.MaintenanceContinuation == nil || req.ExpectedGeneration != req.MaintenanceContinuation.ExpectedGeneration {
				err = errors.New("updater: exact continuation and generation required")
			} else {
				err = s.ContinueCommittedMaintenance(r.Context(), *req.Maintenance, *req.MaintenanceContinuation)
			}
		case "/maintenance/link":
			if req.MaintenanceReceipt == nil {
				err = errors.New("updater: maintenance receipt required")
			} else {
				err = s.LinkCommittedMaintenance(*req.MaintenanceReceipt, req.ExpectedGeneration)
			}
		case "/recovery/plan":
			if req.Recovery == nil {
				err = errors.New("updater: recovery plan required")
			} else {
				err = s.PlanRecovery(r.Context(), *req.Recovery)
			}
		case "/recovery/apply":
			if req.Recovery == nil {
				err = errors.New("updater: recovery plan required")
			} else {
				err = s.AuthorizeRecovery(r.Context(), *req.Recovery, req.ExpectedGeneration)
			}
		case "/recovery/link":
			if req.Receipt == nil {
				err = errors.New("updater: recovery receipt required")
			} else {
				err = s.LinkRecovery(*req.Receipt, req.ExpectedGeneration)
			}
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
