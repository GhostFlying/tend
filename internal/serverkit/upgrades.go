package serverkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/upgradectl"
)

type UpgradeConfig struct {
	Instance      string           `json:"instance"`
	ControlSocket string           `json:"control_socket"`
	UpdaterSocket string           `json:"updater_socket"`
	ReleaseDir    string           `json:"release_dir"`
	DrainTimeout  cfgfile.Duration `json:"drain_timeout"`
	RestartBudget cfgfile.Duration `json:"restart_budget"`
	GrantDuration cfgfile.Duration `json:"grant_duration"`
}

func prepareUpgradeConfig(cfg *UpgradeConfig, dataDir string) error {
	if cfg == nil {
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("automatic upgrades currently require Linux")
	}
	if cfg.Instance == "" {
		cfg.Instance = "server"
	}
	if cfg.ControlSocket == "" {
		cfg.ControlSocket = filepath.Join(dataDir, "upgrades.sock")
	}
	if cfg.ReleaseDir == "" {
		cfg.ReleaseDir = filepath.Join(dataDir, "releases")
	}
	if cfg.UpdaterSocket == "" {
		return errors.New("upgrades.updater_socket is required")
	}
	for _, p := range []*string{&cfg.ControlSocket, &cfg.ReleaseDir, &cfg.UpdaterSocket} {
		v, err := cfgfile.Path(*p)
		if err != nil {
			return err
		}
		*p = v
	}
	if cfg.DrainTimeout < 0 || cfg.RestartBudget < 0 || cfg.GrantDuration < 0 {
		return errors.New("upgrade timeouts must not be negative")
	}
	return nil
}
func executableSHA256() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		path = "/proc/self/exe"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func prepareUpgradeController(cfg *UpgradeConfig, a *app.App, front *mainthread.Manager, background *mainthread.BackgroundPool, ids protocol.IDGen, log *slog.Logger) (*upgradectl.Controller, error) {
	if cfg == nil {
		return nil, nil
	}
	sum, err := executableSHA256()
	if err != nil {
		return nil, err
	}
	process := os.Getenv("TEND_UPDATER_PROCESS_INSTANCE_ID")
	if process == "" {
		process = ids.New(protocol.PrefixLaunch)
	}
	return upgradectl.New(upgradectl.Config{Instance: cfg.Instance, ProcessInstance: process, BinarySHA256: sum, ControlSocket: cfg.ControlSocket, UpdaterSocket: cfg.UpdaterSocket, ReleaseDir: cfg.ReleaseDir, DrainMillis: time.Duration(cfg.DrainTimeout).Milliseconds(), RestartMillis: time.Duration(cfg.RestartBudget).Milliseconds(), GrantMillis: time.Duration(cfg.GrantDuration).Milliseconds()}, a, ids, func(ctx context.Context) (app.ServerObservation, error) {
		var agents []upgradeDrainer
		if front != nil {
			agents = append(agents, front)
		}
		if background != nil {
			agents = append(agents, background)
		}
		drained, err := allAgentsDrained(ctx, a, agents...)
		if err != nil {
			return app.ServerObservation{}, err
		}
		return app.ServerObservation{Healthy: true, MainDrained: drained}, nil
	}, log)
}

type upgradeDrainer interface {
	UpgradeDrainStatus(context.Context) (bool, error)
}

func allAgentsDrained(ctx context.Context, a *app.App, agents ...upgradeDrainer) (bool, error) {
	for _, agent := range agents {
		drained, err := agent.UpgradeDrainStatus(ctx)
		if err != nil || !drained {
			return false, err
		}
	}
	uncertain, err := a.AgentCompactionsUncertain(ctx)
	return !uncertain && err == nil, err
}
