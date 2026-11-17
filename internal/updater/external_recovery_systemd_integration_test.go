//go:build integration && linux

package updater

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Exercise the shipped CLI, installer, supervisor and server together. The
// bundle is built once outside the test; no compilation occurs during downtime.
func TestExternalRecoveryCompleteRelease(t *testing.T) {
	directory := os.Getenv("TEND_TEST_RECOVERY_BUNDLE")
	if directory == "" {
		t.Skip("requires TEND_TEST_RECOVERY_BUNDLE and a systemd user manager")
	}
	bundle, err := release.Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := bundle.SetArtifact("cli", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	server, err := bundle.Artifact("server", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	root := testkit.SocketDir(t)
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(root, "updater.json")
	serverConfiguration := filepath.Join(root, "server.json")
	socket := filepath.Join(root, "server.sock")
	fixed := filepath.Join(root, "fixed-updater")
	env := filepath.Join(root, "server.env")
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(data, "tend.db")
	ledger, err := store.Open(t.Context(), database, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	err = ledger.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `CREATE TABLE recovery_fixture(value TEXT NOT NULL)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO recovery_fixture(value) VALUES('retained business data')`)
		return err
	})
	if closeErr := ledger.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("TEND_SERVER_SECRET="+hex.EncodeToString(secret)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{InstanceID: "recovery-fixture", Role: "server", StateDir: state, InitialBinary: filepath.Join(bundle.Dir, server.Path), Args: []string{"--config", serverConfiguration}, EnvFiles: []string{env}, DataFiles: []string{filepath.Join(data, "tend.db")}, LogPath: filepath.Join(root, "server.log"), AuthoritySocket: socket, CoordinatorSocket: socket, AuthorizeURL: "http://localhost/updater/authorize", HealthURL: "http://localhost/updater/health", Protocol: bundle.Manifest.Protocol.Max, StorageVersion: bundle.Manifest.Storage.ServerSchema.Max, StopTimeoutSeconds: 60, HealthTimeoutSeconds: 30}
	if err := writeJSON(configuration, cfg); err != nil {
		t.Fatal(err)
	}
	serverConfig := map[string]any{"data_dir": data, "listen": address, "control_socket": socket, "local_owner": "fixture-owner", "owners": []string{"fixture-owner"}, "upgrades": map[string]string{"instance": cfg.InstanceID, "control_socket": socket, "updater_socket": SocketPath(cfg)}}
	if err := writeJSON(serverConfiguration, serverConfig); err != nil {
		t.Fatal(err)
	}
	original := []byte("unreadable previous updater state\n")
	journal := []byte("unreadable previous startup journal\n")
	if err := os.WriteFile(filepath.Join(state, "state.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(state, "self-update"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "self-update", "journal.json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixed, []byte("#!/bin/sh\ntrap '' TERM\nsleep 600 &\nwait\n"), 0700); err != nil {
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
	name := "tend-release-recovery-test-" + id + ".service"
	units := filepath.Join(configRoot, "systemd", "user")
	if err := os.MkdirAll(units, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(units, name)
	if strings.ContainsAny(root, " \n\t") {
		t.Fatal("fixture path is not systemd-safe")
	}
	unit := fmt.Sprintf("[Service]\nType=simple\nExecStart=%s serve --config %s\nKillMode=mixed\nTimeoutStopSec=60\nRestart=always\nStandardOutput=append:%s\nStandardError=append:%s\n", fixed, configuration, filepath.Join(root, "updater.log"), filepath.Join(root, "updater.log"))
	if err := os.WriteFile(file, []byte(unit), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = (systemdUser{}).call(ctx, "kill", "--kill-whom=all", "--signal=SIGKILL", name)
		_, _ = (systemdUser{}).call(ctx, "stop", name)
		_ = os.Remove(file)
		_, _ = (systemdUser{}).call(ctx, "daemon-reload")
		_, _ = (systemdUser{}).call(ctx, "reset-failed", name)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	for _, args := range [][]string{{"daemon-reload"}, {"start", name}} {
		if _, err := (systemdUser{}).call(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	execute := func(flag string) ExternalRecoveryRecord {
		t.Helper()
		command := exec.CommandContext(ctx, filepath.Join(bundle.Dir, cli.Path), "upgrade", "recover", "--release", bundle.Dir, "--config", configuration, "--unit", name, flag)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
		command.WaitDelay = 5 * time.Second
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("recovery CLI: %v: %s", err, raw)
		}
		var record ExternalRecoveryRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("recovery output: %v: %s", err, raw)
		}
		return record
	}
	start := time.Now()
	first := execute("--force")
	t.Logf("recovery including preparation: %s", time.Since(start))
	installed, err := readState(filepath.Join(state, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Phase != "complete" || installed.Operation == nil || installed.Operation.Phase != "committed" || installed.CommittedProof == nil || !installed.CommittedProof.ExternalRecovery || installed.Operation.BackupDir == "" {
		t.Fatal("unknown storage was not preserved before durable installation", first, installed.Operation)
	}
	for _, old := range []struct {
		path string
		body []byte
	}{{"state.json", original}, {"self-update/journal.json", journal}} {
		raw, err := os.ReadFile(filepath.Join(first.Archive, old.path))
		if err != nil || string(raw) != string(old.body) {
			t.Fatal("original state was not retained", old.path, err)
		}
	}
	for _, flag := range []string{"--status", "--force"} {
		next := execute(flag)
		current, err := readState(filepath.Join(state, "state.json"))
		if err != nil || next.RequestID != first.RequestID || next.Phase != "complete" || current.Process != installed.Process {
			t.Fatal("retry replaced the original committed execution", next, err)
		}
	}
	retained, err := sql.Open("sqlite", "file:"+database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	var value string
	if err := retained.QueryRowContext(ctx, `SELECT value FROM recovery_fixture`).Scan(&value); err != nil || value != "retained business data" {
		t.Fatal("recovery lost existing data", value, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/session", strings.NewReader(`{"client_id":"release-rehearsal","scope":"owner"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("installed server is not ready", response.Status)
	}
}
