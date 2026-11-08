// Command tend-maintainer runs standalone maintenance and bounded audit dispatch.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/maintenance"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: tend-maintainer serve|audit --config FILE")
	}
	action := os.Args[1]
	fs := flag.NewFlagSet("tend-maintainer "+action, flag.ContinueOnError)
	config := fs.String("config", "", "private configuration file")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if action != "serve" && action != "audit" {
		return fmt.Errorf("unknown action %s", action)
	}
	cfg, err := maintenance.ReadConfig(*config)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	opts := maintenance.Options{Now: time.Now, ID: func() string {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		return hex.EncodeToString(b[:])
	}, Native: &maintenance.NativeClient{}}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	opts.RuntimeID = os.Getenv("TEND_UPDATER_PROCESS_INSTANCE_ID")
	opts.ArtifactSHA256 = hex.EncodeToString(sum[:])
	p := maintenance.Probe{URL: cfg.ServerURL, TokenFile: cfg.TokenFile, HTTP: &http.Client{Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if action == "audit" {
		return maintenance.Audit(ctx, cfg, opts)
	}
	c, err := maintenance.Open(ctx, cfg, opts)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Run(ctx, p)
}
