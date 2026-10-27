// Command tend is the owner command line. All logic is in internal/cli.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	home, _ := os.UserHomeDir()
	configDir := os.Getenv(cli.ConfigDirEnv)
	if configDir == "" {
		configDir = filepath.Join(home, ".config", "tend")
	}
	e := &cli.Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Home: home, ConfigDir: configDir,
		HTTP: &http.Client{Timeout: 60 * time.Second, Transport: cli.Transport()},
		IDs:  protocol.NewULIDGen(time.Now, nil),
	}
	os.Exit(cli.Main(ctx, e, os.Args[1:]))
}
