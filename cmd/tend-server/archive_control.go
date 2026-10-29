package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
)

func archiveControlSocket(configured string, upgrade *upgradeConfig, dataDir string) (string, error) {
	if runtime.GOOS == "windows" {
		if configured != "" {
			return "", errors.New("local archive control requires a Unix socket host")
		}
		return "", nil
	}
	if configured == "" {
		if upgrade != nil {
			return upgrade.ControlSocket, nil
		}
		configured = filepath.Join(dataDir, "upgrades.sock")
	}
	socket, err := cfgfile.Path(configured)
	if err != nil {
		return "", err
	}
	if upgrade != nil && socket != upgrade.ControlSocket {
		return "", errors.New("control_socket and upgrades.control_socket must select the same local listener")
	}
	return socket, nil
}

func shutdownHTTP(ctx context.Context, server *http.Server, budget time.Duration, log *slog.Logger) error {
	bounded, cancel := context.WithTimeout(ctx, budget)
	err := server.Shutdown(bounded)
	cancel()
	if err == nil {
		return nil
	}
	log.ErrorContext(ctx, "HTTP shutdown has not drained accepted requests; waiting without forcing connections closed", "err", err)
	// Readers/store must stay open until real requests finish. The original
	// deadline failure remains visible even if a later drain succeeds.
	return errors.Join(err, server.Shutdown(ctx))
}
