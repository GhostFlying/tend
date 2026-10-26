// tend-updater supervises one configured service. It is installed explicitly;
// models never execute the update steps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func load(path string) (updater.Config, error) {
	var cfg updater.Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("updater: trailing configuration data")
	}
	return cfg, nil
}
func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tend-updater serve|bootstrap|upgrade|restart|status|retry|cancel|verify --config <file>")
	}
	command := args[0]
	flags := flag.NewFlagSet("tend-updater "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", os.Getenv("TEND_UPDATER_CONFIG"), "fixed supervisor configuration")
	bundle := flags.String("release", "", "release bundle directory")
	id := flags.String("upgrade-id", "", "stable coordinator upgrade identity")
	candidateConfig := flags.String("candidate", "", "private candidate service configuration")
	previousConfig := flags.String("previous-sha256", "", "expected current configuration digest")
	targetConfig := flags.String("sha256", "", "candidate configuration digest")
	manifest := flags.String("manifest-sha256", "", "pinned maintenance manifest digest")
	providers := flags.String("providers", "", "comma-separated required providers")
	pid := flags.Int("pid", 0, "existing idle service PID")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("updater: unexpected positional arguments")
	}
	if command == "verify" {
		b, err := release.Load(*bundle)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"release_id": b.Manifest.ReleaseID, "manifest_sha256": b.ManifestSHA256, "artifacts": len(b.Manifest.Artifacts)})
	}
	if *config == "" {
		return errors.New("updater: --config is required")
	}
	cfg, err := load(*config)
	if err != nil {
		return err
	}
	if command == "serve" {
		var authority updater.Authority
		if cfg.AuthorizeURL != "" || cfg.HealthURL != "" {
			authority, err = updater.NewHTTPAuthority(cfg)
			if err != nil {
				return err
			}
		}
		s, err := updater.Open(cfg, updater.Options{Authority: authority})
		if err != nil {
			return err
		}
		defer s.Close()
		return s.Serve(ctx)
	}
	switch command {
	case "bootstrap", "upgrade", "restart", "status", "retry", "cancel", "stage-config":
	default:
		return errors.New("updater: unknown command")
	}
	if command == "upgrade" {
		if *bundle == "" {
			return errors.New("updater: --release is required")
		}
		abs, err := filepath.Abs(*bundle)
		if err != nil {
			return err
		}
		*bundle = abs
		b, err := release.Load(*bundle)
		if err != nil {
			return err
		}
		if *id == "" {
			*id = "release-" + b.ManifestSHA256[:32]
		}
	}
	if command == "upgrade" && cfg.Role == "server" {
		if cfg.CoordinatorSocket == "" {
			return errors.New("updater: server upgrade requires coordinator_socket; use the local release coordinator")
		}
		requestID := protocol.NewULIDGen(time.Now, nil).New(protocol.PrefixRequest)
		fmt.Fprintf(os.Stderr, "Upgrade %s; request %s\n", *id, requestID)
		return startCoordinatedUpgrade(ctx, cfg.CoordinatorSocket, protocol.UpgradeStartRequest{RequestID: requestID, UpgradeID: *id, Release: *bundle}, out)
	}
	if command == "restart" && *id == "" {
		return errors.New("updater: restart requires a stable --upgrade-id")
	}
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	request := updater.ControlRequest{ID: *id, Release: *bundle, PID: *pid}
	if command == "stage-config" {
		required := []string{}
		if *providers != "" {
			required = strings.Split(*providers, ",")
		}
		request.Configuration = &protocol.RunnerConfiguration{PreviousSHA256: *previousConfig, SHA256: *targetConfig, Providers: required}
		request.Candidate = *candidateConfig
		request.ManifestSHA256 = *manifest
	}
	state, err := updater.Call(callCtx, cfg, command, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(state)
}

func startCoordinatedUpgrade(ctx context.Context, socket string, request protocol.UpgradeStartRequest, out io.Writer) error {
	if !filepath.IsAbs(socket) {
		return errors.New("updater: coordinator socket must be an absolute path")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("updater: coordinator redirects forbidden")
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/upgrades/start", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("updater: release start outcome unknown; inspect status and preserve the request ID")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("updater: release coordinator rejected start with HTTP %d", response.StatusCode)
	}
	var view protocol.UpgradeView
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&view); err != nil {
		return errors.New("updater: invalid release coordinator response; inspect status before retrying")
	}
	return json.NewEncoder(out).Encode(view)
}
