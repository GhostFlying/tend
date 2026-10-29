package upgradectl

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ServeLocal shares the existing private control socket with optional archive
// commands. It creates no installer, credentials, or background timer.
func ServeLocal(ctx context.Context, socket string, handler http.Handler) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("upgradectl: control path is not a socket")
		}
		probe, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			_ = probe.Close()
			return errors.New("upgradectl: another local controller is active")
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0o600); err != nil {
		return err
	}

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second}
	finished := make(chan struct{})
	watchDone := make(chan struct{})
	defer func() { close(finished); <-watchDone }()
	stopped := make(chan error, 1)
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			stopped <- server.Shutdown(context.Background())
		case <-finished:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return <-stopped
	}
	return err
}
