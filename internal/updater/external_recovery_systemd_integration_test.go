//go:build integration && linux

package updater

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the real service-manager boundary. The isolated service deliberately
// ignores TERM and has no working updater socket; its child must die with it.
func TestExternalRecoverySystemdStopsUnresponsiveServiceAndChildren(t *testing.T) {
	root := t.TempDir()
	configuration := filepath.Join(root, "updater.json")
	executable := filepath.Join(root, "fixed-updater")
	if err := os.WriteFile(configuration, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntrap '' TERM\nsleep 600 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	name := "tend-recovery-test-" + id + ".service"
	units := filepath.Join(configRoot, "systemd", "user")
	if err := os.MkdirAll(units, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(units, name)
	if strings.ContainsAny(root, " \n\t") {
		t.Fatal("fixture path is not systemd-safe")
	}
	unit := "[Unit]\nDescription=Isolated updater recovery test\n[Service]\nType=simple\nExecStart=" + executable + " serve --config " + configuration + "\nKillMode=mixed\nTimeoutStopSec=60\nRestart=always\n"
	if err := os.WriteFile(file, []byte(unit), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = (systemdUser{}).call(ctx, "kill", "--kill-whom=all", "--signal=SIGKILL", name)
		_, _ = (systemdUser{}).call(ctx, "stop", name)
		_ = os.Remove(file)
		_, _ = (systemdUser{}).call(ctx, "daemon-reload")
		_, _ = (systemdUser{}).call(ctx, "reset-failed", name)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if _, err = (systemdUser{}).call(ctx, "daemon-reload"); err != nil {
		t.Fatal(err)
	}
	if _, err = (systemdUser{}).call(ctx, "start", name); err != nil {
		t.Fatal(err)
	}
	manager := externalSystemd{}
	observed, err := manager.Inspect(ctx, name, configuration)
	if err != nil || observed.Cgroup == "" {
		t.Fatal(observed, err)
	}
	started := time.Now()
	stopCtx, stopCancel := context.WithTimeout(ctx, 5*time.Second)
	defer stopCancel()
	if err := manager.Stop(stopCtx, observed, configuration); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 5*time.Second {
		t.Fatal("recovery waited for the broken service's normal shutdown timeout")
	}
	if err := manager.Start(ctx, observed, configuration); err != nil {
		t.Fatal(err)
	}
	restarted, err := manager.Inspect(ctx, name, configuration)
	if err != nil || restarted.Digest != observed.Digest {
		t.Fatal(restarted, err)
	}
	if err := manager.Stop(ctx, restarted, configuration); err != nil {
		t.Fatal(err)
	}
}
