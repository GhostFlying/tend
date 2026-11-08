package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func (c *Controller) frozen() bool {
	return c.state.Upgrade != nil && c.state.Upgrade.Phase != "planned" && c.state.Upgrade.Phase != "rejected" && c.state.Upgrade.Phase != "completed" && c.state.Upgrade.Phase != "rolled_back" && c.state.Upgrade.Phase != "cancelled"
}

var errUpdaterRejected = errors.New("maintenance: updater rejected the installation")

func (c *Controller) planUpgrade(id, dir string) (*protocol.MaintenanceUpgrade, error) {
	if c.cfg.UpdaterSocket == "" {
		return nil, errors.New("maintenance: no fixed updater configured")
	}
	if c.frozen() {
		return nil, errors.New("maintenance: an installation is unfinished")
	}
	b, err := release.LoadComponent(dir)
	if err != nil {
		return nil, err
	}
	a, err := b.Artifact("maintainer", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if !b.Manifest.Storage.ServerSchema.Contains(int64(c.state.Format)) {
		return nil, errors.New("maintenance: component cannot read current state")
	}
	return &protocol.MaintenanceUpgrade{ID: id, ReleaseDir: dir, ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: a.SHA256, Phase: "planned"}, nil
}

func (c *Controller) updaterRequest(ctx context.Context, method, path string, input any) (updater.State, error) {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.cfg.UpdaterSocket)
	}}
	defer tr.CloseIdleConnections()
	raw, err := json.Marshal(input)
	if err != nil {
		return updater.State{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bytes.NewReader(raw))
	if err != nil {
		return updater.State{}, err
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return updater.State{}, errors.New("maintenance: updater request outcome unknown")
	}
	defer resp.Body.Close()
	var result updater.ControlResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return updater.State{}, err
	}
	if resp.StatusCode != 200 || result.Error != "" {
		if resp.StatusCode == http.StatusConflict && result.Error != "" {
			return result.State, errUpdaterRejected
		}
		return result.State, errors.New("maintenance: updater request outcome unknown; inspect updater status")
	}
	return result.State, nil
}

func (c *Controller) advanceUpgrade(ctx context.Context) error {
	u := c.state.Upgrade
	if u.Phase == "requested" {
		u.Phase = "submitting"
		if err := c.save(ctx); err != nil {
			return err
		}
		s, current, err := c.requestUpgradeUnlocked(ctx, u, http.MethodPost, "/upgrade", updater.ControlRequest{ID: u.ID, Release: u.ReleaseDir})
		if !current {
			return err
		}
		_, terminal := s.Terminals[u.ID]
		_, historical := s.OperationHistory[u.ID]
		if errors.Is(err, errUpdaterRejected) && u.Binding == nil && s.InstanceID == c.cfg.InstanceID && !terminal && !historical && (s.Operation == nil || s.Operation.ID != u.ID) && (s.CommittedProof == nil || s.CommittedProof.Binding.UpgradeID != u.ID) {
			u.Phase = "rejected"
			if saveErr := c.save(ctx); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	s, current, err := c.requestUpgradeUnlocked(ctx, u, http.MethodGet, "/status", nil)
	if !current {
		return err
	}
	if err != nil {
		return err
	}
	if s.InstanceID != c.cfg.InstanceID {
		return errors.New("maintenance: updater instance does not match installation")
	}
	matches := func(b protocol.UpgradeBinding) bool {
		return b.InstanceID == c.cfg.InstanceID && b.UpgradeID == u.ID && b.ReleaseID == u.ReleaseID && b.ManifestSHA256 == u.ManifestSHA256 && b.ArtifactSHA256 == u.ArtifactSHA256 && (u.Binding == nil || *u.Binding == b)
	}
	if receipt, ok := s.Terminals[u.ID]; ok && receipt.Phase == "cancelled" {
		preaccept := u.Binding == nil && receipt.Binding == (protocol.UpgradeBinding{InstanceID: c.cfg.InstanceID, UpgradeID: u.ID})
		if (preaccept || matches(receipt.Binding)) && s.Process.LaunchID == c.opts.RuntimeID && s.Process.SHA256 == c.opts.ArtifactSHA256 && s.Current.Artifact.SHA256 == c.opts.ArtifactSHA256 {
			u.Phase = "cancelled"
			return c.save(ctx)
		}
	}
	if receipt, ok := s.Terminals[u.ID]; ok && receipt.Phase == "committed" && matches(receipt.Binding) {
		proof := s.CommittedProof
		if proof == nil || proof.Binding != receipt.Binding || proof.Version != s.Current || proof.Process != s.Process || proof.Version.ManifestSHA256 != u.ManifestSHA256 || proof.Version.Artifact.SHA256 != u.ArtifactSHA256 || s.Process.LaunchID != c.opts.RuntimeID || s.Process.SHA256 != c.opts.ArtifactSHA256 || c.opts.ArtifactSHA256 != u.ArtifactSHA256 {
			return errors.New("maintenance: retained installation receipt does not match the running process")
		}
		u.Phase = "completed"
		return c.save(ctx)
	}
	if receipt, ok := s.Terminals[u.ID]; ok && receipt.Phase == "rolled_back" && matches(receipt.Binding) {
		proof := s.RollbackProof
		if proof == nil || proof.Binding != receipt.Binding || proof.Version != s.Current || proof.Process != s.Process || s.Process.LaunchID != c.opts.RuntimeID || s.Process.SHA256 != c.opts.ArtifactSHA256 || proof.Version.Artifact.SHA256 != c.opts.ArtifactSHA256 {
			return errors.New("maintenance: retained rollback receipt does not match the running process")
		}
		u.Phase = "rolled_back"
		return c.save(ctx)
	}
	if s.Operation == nil || s.Operation.ID != u.ID || !matches(s.Operation.Binding) || s.Operation.Candidate.ManifestSHA256 != u.ManifestSHA256 || s.Operation.Candidate.Artifact.SHA256 != u.ArtifactSHA256 {
		return errors.New("maintenance: original updater installation requires reconciliation")
	}
	if s.Operation.Phase == "committed" {
		if s.Process.LaunchID != c.opts.RuntimeID || s.Process.SHA256 != c.opts.ArtifactSHA256 {
			return errors.New("maintenance: updater receipt belongs to another process")
		}
		u.Phase = "completed"
		return c.save(ctx)
	}
	if s.Operation.Phase == "rolled_back" || s.Operation.Phase == "cancelled" {
		if s.Process.LaunchID != c.opts.RuntimeID || s.Process.SHA256 != c.opts.ArtifactSHA256 || s.Current.Artifact.SHA256 != c.opts.ArtifactSHA256 {
			return errors.New("maintenance: rollback or cancellation belongs to another running process")
		}
		if s.Operation.Phase == "rolled_back" && (s.RollbackProof == nil || s.RollbackProof.Binding != s.Operation.Binding || s.RollbackProof.Process != s.Process) {
			return errors.New("maintenance: rollback receipt is unconfirmed")
		}
		u.Phase = s.Operation.Phase
		return c.save(ctx)
	}
	return nil
}

// The caller holds c.mu on entry and return. Updater callbacks need the same
// mutex, so the RPC runs outside it. Only the same durable plan/phase may consume
// its result; callbacks may attach that plan's binding while the RPC is running.
func (c *Controller) requestUpgradeUnlocked(ctx context.Context, u *protocol.MaintenanceUpgrade, method, path string, input any) (updater.State, bool, error) {
	phase := u.Phase
	c.mu.Unlock()
	s, err := c.updaterRequest(ctx, method, path, input)
	c.mu.Lock()
	if c.fault != nil {
		return updater.State{}, false, c.fault
	}
	if c.state.Upgrade != u || u.Phase != phase {
		return updater.State{}, false, nil
	}
	return s, true, err
}

func (c *Controller) authorizeUpgrade(ctx context.Context, b protocol.UpgradeBinding) (protocol.UpgradeGrant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.state.Upgrade
	if c.fault != nil || u == nil || !c.frozen() || b.InstanceID != c.cfg.InstanceID || b.UpgradeID != u.ID || b.ReleaseID != u.ReleaseID || b.ManifestSHA256 != u.ManifestSHA256 || b.ArtifactSHA256 != u.ArtifactSHA256 || b.ProcessInstanceID == "" {
		return protocol.UpgradeGrant{}, errors.New("maintenance: installation does not match the accepted plan")
	}
	if u.Binding != nil && *u.Binding != b {
		return protocol.UpgradeGrant{}, errors.New("maintenance: conflicting installation binding")
	}
	if u.Binding == nil {
		u.Binding = &b
		if err := c.save(ctx); err != nil {
			return protocol.UpgradeGrant{}, err
		}
	}
	return protocol.UpgradeGrant{Binding: b, Allowed: true, ExpiresAt: c.opts.Now().Add(10 * time.Minute), RemainingMillis: 10 * 60 * 1000}, nil
}

func (c *Controller) upgradeHealth(b protocol.UpgradeBinding) (protocol.UpgradeHealth, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.state.Upgrade
	if c.fault != nil || u == nil || u.Binding == nil || *u.Binding != b || c.opts.RuntimeID != b.ProcessInstanceID || c.opts.ArtifactSHA256 != b.ArtifactSHA256 {
		return protocol.UpgradeHealth{}, errors.New("maintenance: health binding does not match the running candidate")
	}
	return protocol.UpgradeHealth{Binding: b, ServerConfirmed: true, ServerRevision: c.state.Revision, Healthy: true, HeartbeatSequence: uint64(c.opts.Now().UnixNano()), NewWorkAccepted: u.Phase == "completed", AdmissionFenced: c.frozen(), ControlRevision: c.state.Revision}, nil
}

func (c *Controller) upgradeHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /maintenance/upgrade-status", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(c.Status().Upgrade) })
	for _, path := range []string{"/updater/authorize", "/updater/health"} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var b protocol.UpgradeBinding
			if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b) != nil {
				http.Error(w, "invalid binding", 400)
				return
			}
			var v any
			var err error
			if path == "/updater/authorize" {
				v, err = c.authorizeUpgrade(r.Context(), b)
			} else {
				v, err = c.upgradeHealth(b)
			}
			if err != nil {
				controlError(w, http.StatusConflict, protocol.CodeInvalidTransition, "updater binding is not admitted by this maintenance process")
				return
			}
			_ = json.NewEncoder(w).Encode(v)
		})
	}
}
