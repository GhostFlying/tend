package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/usagemaint"
)

func runUsageCommand(args []string, out, errout io.Writer) int {
	if len(args) == 0 || args[0] != "reconcile" {
		fmt.Fprintln(errout, "usage: tend-runner usage reconcile --native-home <dir> --provider codex|traex [--owner | --config <file>] [--apply]")
		return 2
	}
	fs := flag.NewFlagSet("usage reconcile", flag.ContinueOnError)
	fs.SetOutput(errout)
	path := fs.String("config", "", "runner configuration")
	home := fs.String("native-home", "", "explicit native provider home")
	provider := fs.String("provider", "", "codex or traex")
	owner := fs.Bool("owner", false, "reconcile registered hosted conversations with owner credentials")
	apply := fs.Bool("apply", false, "apply the preview using durable request receipts")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *home == "" || (*provider != "codex" && *provider != "traex") {
		return 2
	}
	c := usagemaint.Config{NativeHome: *home, Provider: *provider, Apply: *apply}
	var err error
	if *owner {
		dir := os.Getenv("TEND_CONFIG_DIR")
		if dir == "" {
			h, e := os.UserHomeDir()
			if e != nil {
				err = e
			} else {
				dir = filepath.Join(h, ".config", "tend")
			}
		}
		var settings struct {
			Server string `json:"server"`
		}
		var credentials struct {
			Owner      string `json:"owner_token"`
			Deployment string `json:"deployment_id"`
		}
		if err == nil {
			err = readUsageJSON(filepath.Join(dir, "config.json"), &settings)
		}
		if err == nil {
			err = readUsageJSON(filepath.Join(dir, "credentials.json"), &credentials)
		}
		c.Server, c.Token, c.DeploymentID = settings.Server, credentials.Owner, credentials.Deployment
		c.Journal = filepath.Join(dir, "usage-reconcile.sqlite")
	} else {
		var cfg config
		if *path == "" {
			err = errors.New("--config is required for host reconciliation")
		} else {
			err = cfgfile.Load(*path, &cfg)
		}
		c.Server, c.HostID, c.Token = cfg.ServerURL, cfg.HostID, os.Getenv("TEND_HOST_TOKEN")
		if err == nil {
			h := cfg.Home
			if h == "" {
				h = "~/.tend"
			}
			h, err = cfgfile.Path(h)
			c.Journal = filepath.Join(h, "runner", "journal.db")
			if err == nil && cfg.Tailnet != nil {
				socket := cfg.UpdaterSocket
				if socket == "" {
					socket = filepath.Join(h, "runner", "usage-control", "usage.sock")
				}
				socket, err = cfgfile.Path(socket)
				c.Server = "http://localhost"
				c.HTTP = &http.Client{Timeout: time.Minute, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				}}}
			}
		}
	}
	if err == nil && (c.Server == "" || c.Token == "") {
		err = errors.New("usage reconciliation credentials are unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err == nil {
		err = usagemaint.Run(ctx, c, out)
	}
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
func readUsageJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
