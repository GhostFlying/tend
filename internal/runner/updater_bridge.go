package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

func (r *Runner) updaterListener() (net.Listener, error) {
	path := r.cfg.UpdaterSocket
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("updater bridge directory must be private")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("updater bridge path is not a socket")
		}
		probe, probeErr := net.DialTimeout("unix", path, time.Second)
		if probeErr == nil {
			probe.Close()
			return nil, errors.New("updater bridge already listening")
		}
		// Only a refused connection proves there is no listener. Timeouts and
		// permission failures do not authorize removing another process's socket.
		var op *net.OpError
		if !errors.As(probeErr, &op) || !errors.Is(op.Err, syscall.ECONNREFUSED) {
			return nil, errors.New("updater bridge listener cannot be verified")
		}
		again, err := os.Lstat(path)
		if err != nil || !os.SameFile(info, again) {
			return nil, errors.New("updater bridge socket changed")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (r *Runner) serveUpdater(ctx context.Context, listener net.Listener) {
	server := &http.Server{Handler: r.updaterHandler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-done:
		}
	}()
	defer close(done)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		r.cfg.Log.ErrorContext(ctx, "updater bridge stopped", "err", err)
	}
}

func (r *Runner) updaterHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		operation := ""
		switch q.URL.Path {
		case "/updater/authorize":
			operation = "runner.upgrade_authorize"
		case "/updater/health":
			operation = "runner.upgrade_health"
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var binding protocol.UpgradeBinding
		decoder := json.NewDecoder(io.LimitReader(q.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&binding); err != nil {
			http.Error(w, "invalid binding", http.StatusBadRequest)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid binding", http.StatusBadRequest)
			return
		}
		if binding.InstanceID == "" || binding.InstanceID != r.cfg.UpdaterInstanceID || binding.UpgradeID == "" || binding.ProcessInstanceID == "" {
			http.Error(w, "binding mismatch", http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(q.Context(), 10*time.Second)
		defer cancel()
		if operation == "runner.upgrade_health" {
			// These local comparisons reject wrong candidates early. The server remains
			// authoritative about admission, sequence, lease state and rollback safety.
			r.controlMu.Lock()
			instanceID := r.instanceID
			r.controlMu.Unlock()
			if binding.ProcessInstanceID != instanceID || binding.ArtifactSHA256 != r.cfg.Build.BinarySHA256 || binding.UpgradeID != r.cfg.UpgradeID {
				http.Error(w, "candidate mismatch", http.StatusConflict)
				return
			}
		}
		authorizationKey := ""
		env, err := r.newEnvelope(operation, protocol.Target{Type: "host", ID: r.cfg.HostID}, binding)
		if err == nil && operation == "runner.upgrade_authorize" {
			r.controlMu.Lock()
			revision := r.control.Revision
			r.controlMu.Unlock()
			env.ExpectedControlRevision = &revision
			encoded, _ := json.Marshal(binding)
			digest := sha256.Sum256(encoded)
			authorizationKey = hex.EncodeToString(digest[:])
			var stored protocol.Envelope
			stored, err = r.cfg.Journal.UpdaterAuthorization(ctx, authorizationKey, *env)
			env = &stored
		}
		if err != nil {
			http.Error(w, "authorization journal unavailable", http.StatusServiceUnavailable)
			return
		}
		op, err := r.hostOperation(ctx, env)
		if err != nil {
			if authorizationKey != "" && op.Status == protocol.OpFailed && op.Error != nil {
				if e := r.cfg.Journal.DenyUpdaterAuthorization(ctx, authorizationKey, env.RequestID); e != nil {
					http.Error(w, "authorization journal unavailable", http.StatusServiceUnavailable)
					return
				}
			}
			http.Error(w, "server confirmation unavailable", http.StatusServiceUnavailable)
			return
		}
		if operation == "runner.upgrade_health" {
			var health protocol.UpgradeHealth
			if json.Unmarshal(op.Result, &health) != nil || health.Binding != binding || !health.ServerConfirmed {
				http.Error(w, "health not confirmed", http.StatusServiceUnavailable)
				return
			}
		} else {
			var grant protocol.UpgradeGrant
			if json.Unmarshal(op.Result, &grant) != nil || grant.Binding != binding {
				http.Error(w, "invalid server authorization", http.StatusServiceUnavailable)
				return
			}
			if !grant.Allowed {
				if err := r.cfg.Journal.DenyUpdaterAuthorization(ctx, authorizationKey, env.RequestID); err != nil {
					http.Error(w, "authorization journal unavailable", http.StatusServiceUnavailable)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(op.Result); err != nil {
			r.cfg.Log.DebugContext(ctx, "updater response interrupted", "err", fmt.Errorf("write response: %w", err))
		}
	})
}

// Only a durable terminal proof from the configured supervisor, matching this
// exact process, can be forwarded to the authenticated server heartbeat.
func (r *Runner) updaterProcessReceipt(ctx context.Context) *protocol.UpdaterReceipt {
	if r.cfg.UpdaterControlSocket == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	state, err := updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
	if err != nil {
		return nil
	}
	r.controlMu.Lock()
	instance := r.instanceID
	upgradeID, cancelledID := r.control.UpgradeID, r.control.CancelledUpgradeID
	r.controlMu.Unlock()
	// A prior committed version can remain current while a new batch was
	// cancelled before starting. Report that batch's durable no-start fact
	// instead of allowing the old version's commit proof to hide it. A stale
	// cancellation marker must not hide a different active batch's commit.
	var receipt *protocol.UpdaterReceipt
	if upgradeID != "" {
		receipt = updater.CancelledReceipt(state, upgradeID)
	}
	if upgradeID == "" && cancelledID != "" {
		receipt = updater.CancelledReceipt(state, cancelledID)
	}
	if receipt == nil {
		receipt = updater.ProcessReceipt(state)
	}
	if receipt == nil {
		return nil
	}
	if receipt.CurrentProcessInstanceID != instance || receipt.CurrentSHA256 != r.cfg.Build.BinarySHA256 || receipt.Binding.InstanceID != r.cfg.UpdaterInstanceID {
		return nil
	}
	return receipt
}
