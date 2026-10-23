// Package upgradectl assembles the server's local release control and updater
// authority endpoints. All business transitions remain in app transactions.
package upgradectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

type Backend interface {
	ListCancelledServerUpgrades(context.Context) ([]string, error)
	GetUpgrade(context.Context, string) (store.UpgradeRow, error)
	Execute(context.Context, protocol.Principal, *protocol.Envelope, app.Command) (protocol.Operation, error)
	CurrentUpgrade(context.Context) (protocol.UpgradeView, error)
	TickUpgrade(context.Context, app.ServerObservation) (protocol.UpgradeView, error)
	ServerUpgradeAuthorize(context.Context, protocol.UpgradeBinding) (protocol.UpgradeGrant, error)
	ServerUpgradeHealth(context.Context, protocol.UpgradeBinding) (protocol.UpgradeHealth, error)
}
type Config struct {
	Instance        string
	ProcessInstance string
	BinarySHA256    string
	ControlSocket   string
	UpdaterSocket   string
	ReleaseDir      string
	DrainMillis     int64
	RestartMillis   int64
	GrantMillis     int64
	Interval        time.Duration
}
type Controller struct {
	cfg     Config
	app     Backend
	ids     protocol.IDGen
	observe func(context.Context) (app.ServerObservation, error)
	log     *slog.Logger
}

func New(cfg Config, a Backend, ids protocol.IDGen, observe func(context.Context) (app.ServerObservation, error), log *slog.Logger) (*Controller, error) {
	if cfg.Instance == "" || cfg.ProcessInstance == "" || !release.ValidSHA(cfg.BinarySHA256) || !filepath.IsAbs(cfg.ControlSocket) || !filepath.IsAbs(cfg.UpdaterSocket) || !filepath.IsAbs(cfg.ReleaseDir) || ids == nil || a == nil || observe == nil {
		return nil, errors.New("upgradectl: invalid fixed upgrade configuration")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(cfg.ReleaseDir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cfg.ReleaseDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("upgradectl: release directory must be private")
	}
	return &Controller{cfg: cfg, app: a, ids: ids, observe: observe, log: log}, nil
}
func (c *Controller) Start(ctx context.Context, r protocol.UpgradeStartRequest) (protocol.UpgradeView, error) {
	if !protocol.ValidID(protocol.PrefixRequest, r.RequestID) || !release.ValidID(r.UpgradeID) || !filepath.IsAbs(r.Release) {
		return protocol.UpgradeView{}, errors.New("upgradectl: invalid local release request")
	}
	bundle, err := release.Load(r.Release)
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	server, err := bundle.Artifact("server", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	bundle, err = bundle.CopyTo(filepath.Join(c.cfg.ReleaseDir, r.UpgradeID))
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	payload, err := json.Marshal(map[string]string{"release_id": bundle.Manifest.ReleaseID, "manifest_sha256": bundle.ManifestSHA256})
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	zero := int64(0)
	env := &protocol.Envelope{RequestID: r.RequestID, Operation: "upgrades.start", Target: protocol.Target{Type: "upgrade", ID: r.UpgradeID}, ExpectedControlRevision: &zero, Payload: payload, Reason: "Start the verified immutable local release through the service updater."}
	op, err := c.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, app.StartUpgrade{Manifest: bundle.Manifest, ManifestSHA256: bundle.ManifestSHA256, ManifestSize: int64(len(bundle.ManifestBytes)), ServerHash: server.SHA256, ServerInstance: c.cfg.Instance, DrainMillis: c.cfg.DrainMillis, RestartMillis: c.cfg.RestartMillis, GrantMillis: c.cfg.GrantMillis})
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	if op.Error != nil {
		return protocol.UpgradeView{}, op.Error
	}
	var v protocol.UpgradeView
	err = json.Unmarshal(op.Result, &v)
	return v, err
}
func (c *Controller) Change(ctx context.Context, action string, r protocol.UpgradeChangeRequest) (protocol.UpgradeView, error) {
	if !protocol.ValidID(protocol.PrefixRequest, r.RequestID) || !release.ValidID(r.UpgradeID) || r.ExpectedControlRevision < 1 {
		return protocol.UpgradeView{}, errors.New("upgradectl: invalid local control request")
	}
	payload, err := json.Marshal(map[string]string{"action": action})
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	env := &protocol.Envelope{RequestID: r.RequestID, Operation: "upgrades.control", Target: protocol.Target{Type: "upgrade", ID: r.UpgradeID}, ExpectedControlRevision: &r.ExpectedControlRevision, Payload: payload, Reason: "Apply the owner's local release control with the current revision."}
	op, err := c.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, app.ControlUpgrade{})
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	if op.Error != nil {
		return protocol.UpgradeView{}, op.Error
	}
	var v protocol.UpgradeView
	err = json.Unmarshal(op.Result, &v)
	return v, err
}
func (c *Controller) tick(ctx context.Context) error {
	if err := c.cancelStaged(ctx); err != nil {
		return err
	}
	observed, err := c.observe(ctx)
	if err != nil {
		return err
	}
	observed.InstanceID = c.cfg.Instance
	observed.ProcessInstanceID = c.cfg.ProcessInstance
	observed.BinarySHA256 = c.cfg.BinarySHA256
	observed.UpdaterReceipt = c.rollbackReceipt(ctx)
	v, err := c.app.TickUpgrade(ctx, observed)
	if err != nil {
		return err
	}
	if v.Status != "running" || v.Phase != "server_ready" || !observed.MainDrained {
		return nil
	}
	// The updater persists/reconciles its process action. Repeated controller
	// ticks use the same batch ID and the same immutable staged directory.
	_, err = updater.CallSocket(ctx, c.cfg.UpdaterSocket, "upgrade", updater.ControlRequest{ID: v.UpgradeID, Release: filepath.Join(c.cfg.ReleaseDir, v.UpgradeID)})
	return err
}
func decode(r *http.Request, out any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("upgradectl: trailing request data")
	}
	return nil
}
func (c *Controller) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var value any
		var err error
		if r.URL.Path == "/upgrades/status" && r.Method == http.MethodGet {
			value, err = c.app.CurrentUpgrade(r.Context())
		} else if r.Method == http.MethodPost {
			switch r.URL.Path {
			case "/upgrades/start":
				var request protocol.UpgradeStartRequest
				if err = decode(r, &request); err == nil {
					value, err = c.Start(r.Context(), request)
				}
			case "/upgrades/retry", "/upgrades/cancel":
				var request protocol.UpgradeChangeRequest
				if err = decode(r, &request); err == nil {
					action := "retry"
					if r.URL.Path == "/upgrades/cancel" {
						action = "cancel"
					}
					value, err = c.Change(r.Context(), action, request)
				}
			case "/updater/authorize":
				var binding protocol.UpgradeBinding
				if err = decode(r, &binding); err == nil {
					value, err = c.app.ServerUpgradeAuthorize(r.Context(), binding)
				}
			case "/updater/health":
				var binding protocol.UpgradeBinding
				if err = decode(r, &binding); err == nil {
					if binding.InstanceID != c.cfg.Instance || binding.ProcessInstanceID != c.cfg.ProcessInstance || binding.ArtifactSHA256 != c.cfg.BinarySHA256 {
						err = errors.New("upgradectl: health belongs to another process")
					} else {
						health, healthErr := c.app.ServerUpgradeHealth(r.Context(), binding)
						err = healthErr
						if healthErr == nil {
							observed, observeErr := c.observe(r.Context())
							if observeErr != nil || !observed.Healthy || !observed.MainDrained {
								health.Healthy = false
							}
							value = health
						}
					}
				}
			default:
				w.WriteHeader(http.StatusNotFound)
				return
			}
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			c.log.WarnContext(r.Context(), "local upgrade operation rejected", "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "upgrade operation rejected; inspect release status"})
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	})
}
func (c *Controller) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(c.cfg.ControlSocket), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(c.cfg.ControlSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("upgradectl: control path is not a socket")
		}
		probe, err := net.DialTimeout("unix", c.cfg.ControlSocket, time.Second)
		if err == nil {
			_ = probe.Close()
			return errors.New("upgradectl: another local controller is active")
		}
		if err := os.Remove(c.cfg.ControlSocket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", c.cfg.ControlSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(c.cfg.ControlSocket)
	if err := os.Chmod(c.cfg.ControlSocket, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(c.cfg.Interval)
		defer ticker.Stop()
		for {
			if err := c.tick(ctx); err != nil && ctx.Err() == nil {
				c.log.WarnContext(ctx, "upgrade reconciliation deferred", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() { <-ctx.Done(); _ = server.Close() }()
	err = server.Serve(listener)
	cancel()
	_ = server.Close()
	<-done
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}

// rollbackReceipt trusts only the configured private supervisor socket, then
// binds its terminal proof to this controller's actual running executable.
func (c *Controller) rollbackReceipt(ctx context.Context) *protocol.UpdaterReceipt {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	response, err := updater.CallSocket(ctx, c.cfg.UpdaterSocket, "status", updater.ControlRequest{})
	if err != nil {
		return nil
	}
	receipt := updater.RollbackReceipt(response)
	if receipt == nil || receipt.Binding.InstanceID != c.cfg.Instance || receipt.CurrentProcessInstanceID != c.cfg.ProcessInstance || receipt.CurrentSHA256 != c.cfg.BinarySHA256 {
		return nil
	}
	return receipt
}

// cancelStaged follows the local operation's original batch, even when a newer
// batch is already latest. The supervisor CAS prevents a raced stop from being
// treated as safely unsent cancellation.
func (c *Controller) cancelStaged(ctx context.Context) error {
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	state, err := updater.CallSocket(check, c.cfg.UpdaterSocket, "status", updater.ControlRequest{})
	if err != nil {
		return nil
	}
	if state.InstanceID != c.cfg.Instance {
		return errors.New("upgradectl: updater belongs to another logical instance")
	}
	ids, err := c.app.ListCancelledServerUpgrades(check)
	if err != nil {
		return err
	}
	// Include the bound historical batch independently of the latest view.
	if op := state.Operation; op != nil && op.Kind == "upgrade" && op.Phase == "staged" {
		batch, err := c.app.GetUpgrade(check, op.Binding.UpgradeID)
		if err != nil {
			return err
		}
		if batch.View.Status == "cancelled" {
			found := false
			for _, id := range ids {
				if id == op.ID {
					found = true
				}
			}
			if !found {
				ids = append(ids, op.ID)
			}
		}
	}
	for _, id := range ids {
		if receipt, ok := state.Terminals[id]; ok && receipt.Binding.UpgradeID == id && receipt.Binding.InstanceID == c.cfg.Instance && (receipt.Phase == "cancelled" || receipt.Phase == "committed" || receipt.Phase == "rolled_back") {
			continue
		}
		if op := state.Operation; op != nil && op.ID == id {
			if op.Kind != "upgrade" || op.Phase != "staged" {
				continue
			}
			b := op.Binding
			if b.InstanceID != c.cfg.Instance || b.UpgradeID != op.ID || b.ProcessInstanceID == "" || state.Process.LaunchID != c.cfg.ProcessInstance || state.Process.SHA256 != c.cfg.BinarySHA256 || state.Current.Artifact.SHA256 != c.cfg.BinarySHA256 {
				return errors.New("upgradectl: staged updater identity mismatch")
			}
			batch, err := c.app.GetUpgrade(check, b.UpgradeID)
			if err != nil {
				return err
			}
			if batch.View.UpgradeID != b.UpgradeID || batch.Manifest.ReleaseID != b.ReleaseID || batch.ManifestSHA256 != b.ManifestSHA256 || batch.ServerInstance != b.InstanceID || batch.ServerHash != b.ArtifactSHA256 {
				return errors.New("upgradectl: staged updater batch identity mismatch")
			}
		}
		state, err = updater.CallSocket(check, c.cfg.UpdaterSocket, "cancel", updater.ControlRequest{ID: id, ExpectedPhase: "staged"})
		if err != nil {
			return err
		}
		if state.InstanceID != c.cfg.Instance || !updater.Cancelled(state, id) {
			return errors.New("upgradectl: cancellation lacks durable terminal proof")
		}
	}
	return nil
}
